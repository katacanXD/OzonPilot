# Copilot instructions for OzonPilot (CLIForOZON)

## Project overview

The product is **OzonPilot** (binary `OzonPilot.exe`); the repository and Go module are still named `CLIForOZON`. It is a Go command-line application that analyzes Ozon seller inventory and recommends FBO replenishment while tracking FBS stock. The module is `CLIForOZON` and requires Go 1.26 as declared in `go.mod`. Runtime output and comments are primarily in Russian; preserve that user-facing language when changing CLI messages.

## Build and run

Run the application against the Ozon API:

```bash
go run ./cmd/app/main.go -prodazhi 28 -postavka 25
```

Build the Windows executable:

```powershell
go build -ldflags="-s -w" -o OzonPilot.exe ./cmd/app
.\OzonPilot.exe -postavka 25
```

Build on Linux/macOS:

```bash
go build -ldflags="-s -w" -o OzonPilot ./cmd/app
./OzonPilot -postavka 25
```

The repository has tests for every package (`*_test.go`). Run them with `go test ./...`; a single test can be selected with `go test ./path/to/package -run '^TestName$' -v`. Format Go changes with `gofmt`. .NET/Go module cache may live outside the workspace, so set `GOCACHE` to a writable path when running in a sandbox.

For network-free runs, pass a JSON file containing serialized `[]domain.ProductSupplyRow`:

```bash
go run ./cmd/app/main.go -file path/to/data.json
```

Useful flags are `-postavka`, `-prodazhi`, `-clusters` (comma-separated or `all`), `-placement-file`, `-report-timeout`, `-file`, `-reset-keys`, and `-debug`. The README is the source of truth for their user-facing defaults and descriptions. `-postavka`/`-prodazhi`/`-clusters` defaults are read from `config.yaml` when present; an explicit CLI flag always wins. `-placement-file` accepts either the API placement report or the seller-cabinet export "Стоимость размещения по товарам" (the only source of Ozon's own storage forecast).

## Architecture and data flow

- `cmd/app/main.go` owns CLI flags, logging setup, cluster parsing, and the online/offline execution split.
- `internal/config` uses Viper to create and read `config.yaml`, prompt for `Client-Id` and `Api-Key`, validate credentials with Ozon, and save them under the platform user-config directory (`%APPDATA%\CLIForOZON` on Windows or `~/.config/CLIForOZON` on Linux/macOS).
- `internal/client` contains Ozon HTTP integration and converts API responses into `RawProductData`. It loads the product catalog, SKU/names, FBO stock, FBS stock, and FBO posting history used to calculate daily sales.
- `internal/domain` contains the business model, `CalculateProductRow`, and the storage forecast (`storage.go`). Online data is transformed here into per-cluster supply recommendations and one of `OUT_OF_STOCK`, `NEEDS_FBO`, `NEEDS_FBS`, or `OK`.
- `internal/printer` renders the sorted summary table (currently disabled/commented out in `main.go`) and creates the Excel report. Sorting is by urgency priority, then total supply descending, then sales descending.
- `internal/printer/excel.go` creates a timestamped `.xlsx` report in a `reports` directory beside the executable. The workbook uses an Excel table with column filters and a frozen header row.

Online execution calculates rows from raw API data. Offline `-file` execution loads already-calculated `ProductSupplyRow` values and sends them directly to the printer; it does not recalculate supply recommendations.

## Domain rules to preserve

- FBO stock and daily sales are tracked per selected cluster.
- The target stock is daily sales multiplied by `-postavka`, plus a fixed three-day lead-time safety stock in the current calculation path.
- A product is `OUT_OF_STOCK` only when all selected clusters have no FBO stock, FBS is zero, and demand is positive.
- Status priority is `OUT_OF_STOCK` (1), `NEEDS_FBO` (2), `NEEDS_FBS` (3), and `OK` (4); changes to these values affect both display ordering and summary counts.
- Missing map entries for a selected cluster naturally behave as zero stock or zero sales; preserve this behavior unless validation is intentionally introduced.

## Ozon integration conventions

- Ozon requests are POST requests with `Client-Id`, `Api-Key`, and JSON content headers. Endpoint paths are constants (`pathProductList`, `pathAnalyticsStocks`, ...) and the URL is built by `endpoint(path)`; `baseURL` is overridable only for tests.
- Reliability is a hard requirement: a partial load must never produce a report. `sendRequestWithOpts` throttles all calls (600 ms global, 6 s for `pathAnalyticsStocks`) and retries `429`/`5xx`/network errors with exponential backoff (`isRetryableStatus`); `4xx` other than 429 fail immediately. Keep the throttle and retry policy in place.
- `FetchAllProducts` is fail-fast: SKU, FBO stock, FBS stock, and sales-history failures abort the run with a wrapped error instead of logging a warning. A product missing from the returned stock set also aborts. Do not restore the old warn-and-continue behavior — it produced zero stock and inflated supply.
- `loadFBOStocks` returns `(warehouse→cluster, coveredSKUs, error)` and re-queries failed chunks across multiple passes; `coveredSKUs` distinguishes "no stock" from "data never arrived".
- Product pagination uses Ozon cursors/last IDs, and posting history uses cursors. Preserve termination guards that stop when a cursor repeats or pagination reports no more data.
- FBO stock comes from `POST /v1/analytics/stocks` (`AnalyticsAPI_AnalyticsStocks`), which mirrors the FBO → stock management section of the seller cabinet. Its `available_stock_count` matches the site's "available for sale" column. Do not reintroduce the deprecated `POST /v2/analytics/stock_on_warehouses`: it returns stock without clusters and diverges from the cabinet, which previously under-reported FBO stock.
- `/v1/analytics/stocks` requires an explicit `skus` list (max 100 per call) and returns rows at SKU × warehouse grain with `cluster_name`. Requests are chunked by 100 SKUs. `loadProductSKUs` fills SKU and names via `POST /v3/product/info/list` because `/v3/product/list` returns only `offer_id`/`product_id`.
- Cluster names come straight from the API, so `loadFBOStocks` returns a `warehouse → cluster` map that `loadSalesVelocityByCluster` reuses to attribute posting sales to the same clusters as the stock. Keep that single source of truth rather than re-deriving clusters from warehouse names.
- Ozon names the Moscow cluster `Москва, МО и Дальние регионы`, while the `-clusters` flag and config use the short name `Москва`. `NormalizeClusterName`/`clusterAliases` in `internal/client/ozon.go` map the two and are applied to both API data and the parsed flag; add new aliases there rather than duplicating mapping logic.
- `isMoscowWarehouse` is now only a fallback for warehouses missing from the `warehouse → cluster` map. Add warehouse aliases there rather than duplicating matching logic elsewhere.
- FBS stock requests are chunked in batches of 100 offers.
- Reliability tests live in `internal/client/ozon_test.go` (retries, throttle bypass via `disableThrottle`/`requestOpts`, fail-fast). Run them with `go test ./internal/client/`; keep new request-path tests using `newTestClient` so they don't inherit real Ozon pauses.

## Storage cost and report conventions

- Placement data comes from two sources: the API report (`/v1/report/placement/by-products/create`) and the seller-cabinet export passed via `-placement-file`. `parseTableRows` must handle both: single-row headers (API file) and two-row headers with column groups (cabinet export). Keep the combined-header detection and the `placementColumns` mapping intact when touching the parser.
- The API report has past charges, paid/total unit counts, and the free-placement end date, but no forecast and no volume. Ozon's own forecast («на 28 дней», «до конца месяца») exists only in the cabinet export; when present it is shown verbatim (`OzonStorageForecast28/…Month`, `HasOzonStorageForecast`) and the program's estimate is not displayed.
- The fallback estimate (`domain.StorageForecast`) derives a per-unit daily tariff from past spend and current stock/sales. It is explicitly labeled as an estimate: Ozon charges litres × 2.5 ₽/l/day after a 120/150/365-day free period, which the API data cannot reproduce.
- Placement status is cleared to `-` when `TotalFBOStock == 0`; past charges remain visible (they describe the past).
- Report layout has 5 or 6 base columns depending on forecast availability, with cluster blocks after them. `baseColumnCount(rows)` derives the count; keep every offset relative to it rather than hardcoding column numbers.
- Every cell style must be fully populated. A zero-value `cellSpec` produces an empty fill color (`rgb="FF"`), which Excel renders blue and strict readers reject — see `TestExportToExcelColors`.
- If placement data is missing, `printer.ExportToExcel` receives a placement note and must surface it in the title and the legend instead of silently showing zeros.
- Keep the placement-report request resilient: creation retries, `-report-timeout` wait per attempt, growing poll interval, re-create on `failed`, up to three download attempts, and one full attempt retry.

## File and output conventions

- Keep domain logic independent of CLI and Ozon transport code.
- Keep API response structs and endpoint-specific request types in `internal/client`.
- Use `log/slog` for operational/debug logging and return wrapped errors from lower-level functions.
- Preserve the table's dynamic cluster columns.
- Preserve the typed numeric cells, table filters, and frozen header row in the generated `.xlsx` report; the report directory is created beside the executable at runtime.
- Do not commit API credentials, local configuration, generated executables, or editor files. The repository ignores `.env*`, `*.exe`, `.idea/`, and common temporary/log files.
