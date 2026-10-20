const fs = require("fs");
const path = require("path");
const {
  Document, Packer, Paragraph, TextRun, Table, TableRow, TableCell,
  WidthType, AlignmentType, BorderStyle, ShadingType, VerticalAlign,
  Header, Footer, PageNumber, TableLayoutType, HeightRule, LevelFormat, LineRuleType,
} = require("docx");

/* ---------- палитра ---------- */
const NAVY = "0E2439";
const TEXT = "2C3E50";
const MUTED = "6E8095";
const BLUE = "005BFF";
const BLUE_DARK = "0043C4";
const CYAN = "6FD6FF";
const BORDER = "DDE6F2";
const ZEBRA = "F8FAFD";
const PANEL = "F2F7FF";
const CODE_BG = "F5F8FD";

const CONTENT = 9638; // 11906 - 2*1134

const NONE = { style: BorderStyle.NONE, size: 0, color: "auto" };
const hair = (color = BORDER) => ({ style: BorderStyle.SINGLE, size: 4, color });

const noBorders = () => ({
  top: { ...NONE },
  bottom: { ...NONE },
  left: { ...NONE },
  right: { ...NONE },
  insideHorizontal: { ...NONE },
  insideVertical: { ...NONE },
});

/* ---------- базовые помощники ---------- */
const run = (text, o = {}) => new TextRun({ text, size: 21, color: TEXT, font: "Segoe UI", ...o });
const mono = (text, o = {}) => new TextRun({ text, size: 19, color: "0B45B5", font: "Consolas", ...o });
const P = (children, o = {}) =>
  new Paragraph({
    spacing: { line: 300, after: 0, ...(o.spacing || {}) },
    alignment: o.alignment,
    border: o.border,
    indent: o.indent,
    keepNext: o.keepNext,
    pageBreakBefore: o.pageBreakBefore,
    numbering: o.numbering,
    shading: o.shading,
    children: Array.isArray(children) ? children : [children],
  });
const body = (parts, o = {}) =>
  P(
    (Array.isArray(parts) ? parts : [parts]).map((x) => (typeof x === "string" ? run(x) : x)),
    { alignment: AlignmentType.JUSTIFIED, spacing: { line: 300, after: 160, ...(o.spacing || {}) }, ...o }
  );
const spacer = (h = 160) => new Paragraph({ spacing: { before: 0, after: h, line: 20 }, children: [] });

const caption = (text) =>
  P([run(text, { size: 15, bold: true, color: MUTED, characterSpacing: 40 })], {
    spacing: { before: 220, after: 90, line: 240 },
    keepNext: true,
  });

const h1 = (num, title) =>
  new Paragraph({
    keepNext: true,
    spacing: { before: 520, after: 260, line: 320 },
    border: { bottom: { style: BorderStyle.SINGLE, size: 6, color: BORDER, space: 8 } },
    children: [
      new TextRun({ text: num, size: 34, bold: true, color: BLUE, font: "Segoe UI" }),
      new TextRun({ text: "\u00A0\u00A0\u00A0", size: 34, font: "Segoe UI" }),
      new TextRun({ text: title, size: 34, bold: true, color: NAVY, font: "Segoe UI" }),
    ],
  });

const h2 = (text) =>
  new Paragraph({
    keepNext: true,
    spacing: { before: 340, after: 140, line: 280 },
    border: { left: { style: BorderStyle.SINGLE, size: 18, color: BLUE, space: 8 } },
    indent: { left: 200 },
    children: [new TextRun({ text, size: 24, bold: true, color: NAVY, font: "Segoe UI" })],
  });

const bullet = (parts) =>
  P(
    (Array.isArray(parts) ? parts : [parts]).map((x) => (typeof x === "string" ? run(x) : x)),
    { numbering: { reference: "bullets", level: 0 }, spacing: { line: 290, after: 100 } }
  );

const step = (parts) =>
  P(
    (Array.isArray(parts) ? parts : [parts]).map((x) => (typeof x === "string" ? run(x) : x)),
    { numbering: { reference: "steps", level: 0 }, spacing: { line: 290, after: 120 } }
  );

/* ---------- код ---------- */
function codeBlock(lines) {
  const paras = lines.map((line) => {
    const isComment = line.trim().startsWith("#");
    return new Paragraph({
      spacing: { before: 0, after: 0, line: 265 },
      children: [
        new TextRun({
          text: line.length ? line : " ",
          font: "Consolas",
          size: 19,
          color: isComment ? "7C8FA8" : "12263D",
        }),
      ],
    });
  });
  return new Table({
    width: { size: CONTENT, type: WidthType.DXA },
    columnWidths: [CONTENT],
    layout: TableLayoutType.FIXED,
    borders: { ...noBorders(), left: { style: BorderStyle.SINGLE, size: 18, color: BLUE } },
    rows: [
      new TableRow({
        cantSplit: true,
        children: [
          new TableCell({
            width: { size: CONTENT, type: WidthType.DXA },
            shading: { type: ShadingType.CLEAR, fill: CODE_BG, color: "auto" },
            margins: { top: 150, bottom: 150, left: 250, right: 200 },
            children: paras,
          }),
        ],
      }),
    ],
  });
}

/* ---------- врезка ---------- */
function callout(kind, title, parts) {
  const c =
    kind === "warn"
      ? { fill: "FFF6EA", bar: "F0A33C", label: "C2410C", text: "5B4632" }
      : { fill: "EDF4FF", bar: BLUE, label: BLUE_DARK, text: "263B52" };

  const groups = [];
  let cur = [];
  for (const part of parts) {
    if (typeof part === "string") {
      part.split("\n").forEach((bit, i) => {
        if (i > 0) { if (cur.length) groups.push(cur); cur = []; }
        if (bit) cur.push(new TextRun({ text: bit, size: 19, color: c.text, font: "Segoe UI" }));
      });
    } else {
      cur.push(part);
    }
  }
  if (cur.length) groups.push(cur);

  const paras = groups.map((g, i) =>
    new Paragraph({ spacing: { before: 0, after: i === groups.length - 1 ? 0 : 60, line: 285 }, children: g })
  );

  return new Table({
    width: { size: CONTENT, type: WidthType.DXA },
    columnWidths: [CONTENT],
    layout: TableLayoutType.FIXED,
    borders: { ...noBorders(), left: { style: BorderStyle.SINGLE, size: 24, color: c.bar } },
    rows: [
      new TableRow({
        cantSplit: true,
        children: [
          new TableCell({
            width: { size: CONTENT, type: WidthType.DXA },
            shading: { type: ShadingType.CLEAR, fill: c.fill, color: "auto" },
            margins: { top: 210, bottom: 210, left: 280, right: 240 },
            children: [
              new Paragraph({
                spacing: { before: 0, after: 90, line: 240 },
                children: [new TextRun({ text: title, size: 16, bold: true, color: c.label, characterSpacing: 30, font: "Segoe UI" })],
              }),
              ...paras,
            ],
          }),
        ],
      }),
    ],
  });
}

/* ---------- таблицы ---------- */
function dataTable({ headers, widths, rows, align = [], monoCols = [], headerFill = BLUE }) {
  const alignOf = (v) => (v === "center" ? AlignmentType.CENTER : v === "right" ? AlignmentType.RIGHT : AlignmentType.LEFT);
  const headRow = new TableRow({
    tableHeader: true,
    cantSplit: true,
    children: headers.map((h, i) =>
      new TableCell({
        width: { size: widths[i], type: WidthType.DXA },
        shading: { type: ShadingType.CLEAR, fill: headerFill, color: "auto" },
        margins: { top: 130, bottom: 130, left: 170, right: 170 },
        verticalAlign: VerticalAlign.CENTER,
        children: [
          new Paragraph({
            spacing: { before: 0, after: 0, line: 250 },
            alignment: alignOf(align[i]),
            children: [new TextRun({ text: h, size: 18, bold: true, color: "FFFFFF", font: "Segoe UI" })],
          }),
        ],
      })
    ),
  });

  const bodyRows = rows.map((row, ri) =>
    new TableRow({
      cantSplit: true,
      children: row.map((val, ci) => {
        const o = typeof val === "object" && val !== null ? val : { text: val };
        const fill = o.fill || (ri % 2 === 1 ? ZEBRA : "FFFFFF");
        return new TableCell({
          width: { size: widths[ci], type: WidthType.DXA },
          shading: { type: ShadingType.CLEAR, fill, color: "auto" },
          margins: { top: 120, bottom: 120, left: 170, right: 170 },
          verticalAlign: VerticalAlign.CENTER,
          children: [
            new Paragraph({
              spacing: { before: 0, after: 0, line: 260 },
              alignment: alignOf(o.align || align[ci]),
              children: o.parts
                ? o.parts.map((x) =>
                    typeof x === "string"
                      ? new TextRun({ text: x, size: 18, color: o.color || TEXT, font: "Segoe UI" })
                      : x
                  )
                : [
                    new TextRun({
                      text: o.text,
                      size: o.size || 18,
                      bold: !!o.bold,
                      color: o.color || TEXT,
                      font: o.mono || monoCols.includes(ci) ? "Consolas" : "Segoe UI",
                    }),
                  ],
            }),
          ],
        });
      }),
    })
  );

  return new Table({
    width: { size: CONTENT, type: WidthType.DXA },
    columnWidths: widths,
    layout: TableLayoutType.FIXED,
    borders: { ...noBorders(), insideHorizontal: hair() },
    rows: [headRow, ...bodyRows],
  });
}

/* ---------- титул ---------- */
const PAGE_W = 11906;
const PAGE_H = 16838;

function cover() {
  const cell = new TableCell({
    width: { size: CONTENT, type: WidthType.DXA },
    verticalAlign: VerticalAlign.CENTER,
    shading: { type: ShadingType.CLEAR, fill: BLUE, color: "auto" },
    margins: { top: 900, bottom: 900, left: 900, right: 700 },
    children: [
      P([run("OZON SELLER API   ·   WINDOWS", { size: 16, bold: true, color: "A9CBFF", characterSpacing: 60 })], {
        spacing: { before: 0, after: 900, line: 240, lineRule: LineRuleType.EXACT },
      }),
      P([run("Расчёт поставок", { size: 52, bold: true, color: "FFFFFF" })], {
        spacing: { before: 0, after: 0, line: 660, lineRule: LineRuleType.EXACT },
      }),
      P([run("на Ozon", { size: 52, bold: true, color: CYAN })], {
        spacing: { before: 0, after: 0, line: 660, lineRule: LineRuleType.EXACT },
      }),
      P([run("", { size: 2 })], {
        spacing: { before: 620, after: 500, line: 20, lineRule: LineRuleType.EXACT },
        border: { bottom: { style: BorderStyle.SINGLE, size: 8, color: "2F6BF5", space: 1 } },
      }),
      P([run("Инструкция по работе с OzonPilot.exe", { size: 24, color: "EAF2FF" })], {
        spacing: { before: 0, after: 300, line: 300, lineRule: LineRuleType.EXACT },
      }),
      P(
        [
          run(
            "Остатки FBO и FBS, продажи по кластерам и готовая таблица поставок — в одном Excel-файле.",
            { size: 18, color: "C4DBFF" }
          ),
        ],
        { spacing: { before: 0, after: 1100, line: 300, lineRule: LineRuleType.EXACT } }
      ),
      P([run("Версия 1.0   ·   Октябрь 2026", { size: 16, color: "9FC3F5", characterSpacing: 20 })], {
        spacing: { before: 0, after: 0, line: 240, lineRule: LineRuleType.EXACT },
      }),
    ],
  });

  return new Table({
    width: { size: CONTENT, type: WidthType.DXA },
    columnWidths: [CONTENT],
    layout: TableLayoutType.FIXED,
    borders: noBorders(),
    rows: [new TableRow({ height: { value: 14400, rule: HeightRule.EXACT }, cantSplit: true, children: [cell] })],
  });
}

/* ---------- содержание ---------- */
function contents() {
  const items = [
    ["01", "Зачем это нужно", "что считает утилита и на каких данных"],
    ["02", "Первый запуск", "где взять ключи Ozon и как их сохранить"],
    ["03", "Обычный запуск", "одна команда до готового отчёта"],
    ["04", "Параметры", "запас, окно продаж, кластеры и служебные флаги"],
    ["05", "Кластеры", "полный список и допустимые сокращения"],
    ["06", "Примеры", "готовые команды для типовых задач"],
    ["07", "Результат", "структура Excel-отчёта и расшифровка статусов"],
    ["08", "Если что-то пошло не так", "частые ошибки и что с ними делать"],
  ];

  const rows = items.map(([num, title, desc]) =>
    new TableRow({
      cantSplit: true,
      children: [
        new TableCell({
          width: { size: 800, type: WidthType.DXA },
          margins: { top: 150, bottom: 150, left: 0, right: 100 },
          children: [P([run(num, { size: 20, bold: true, color: BLUE })], { spacing: { before: 0, after: 0, line: 250 } })],
        }),
        new TableCell({
          width: { size: 8838, type: WidthType.DXA },
          margins: { top: 150, bottom: 150, left: 60, right: 0 },
          children: [
            P([run(title, { size: 21, bold: true, color: NAVY })], { spacing: { before: 0, after: 40, line: 260 } }),
            P([run(desc, { size: 17, color: MUTED })], { spacing: { before: 0, after: 0, line: 240 } }),
          ],
        }),
      ],
    })
  );

  return new Table({
    width: { size: CONTENT, type: WidthType.DXA },
    columnWidths: [800, 8838],
    layout: TableLayoutType.FIXED,
    borders: { ...noBorders(), insideHorizontal: hair("EDF2F9") },
    rows,
  });
}

/* ---------- документ ---------- */
const children = [];

/* титул */
children.push(cover());

/* содержание */
children.push(
  P([run("СОДЕРЖАНИЕ", { size: 16, bold: true, color: BLUE, characterSpacing: 60 })], {
    pageBreakBefore: true,
    spacing: { before: 200, after: 140, line: 240 },
  })
);
children.push(
  P([run("Что в инструкции", { size: 34, bold: true, color: NAVY })], {
    spacing: { before: 0, after: 300, line: 360 },
  })
);
children.push(contents());
children.push(spacer(320));

children.push(caption("БЫСТРЫЙ СТАРТ"));
children.push(codeBlock([".\\OzonPilot.exe -clusters all"]));
children.push(spacer(140));
children.push(
  body([
    "Ключи программа спросит один раз при первом запуске. Готовый отчёт появится в папке ",
    mono("reports"),
    " рядом с ",
    mono("OzonPilot.exe"),
    ".",
  ])
);

/* 01 */
children.push(h1("01", "Зачем это нужно"));
children.push(
  body(
    "OzonPilot.exe — консольная утилита для Windows, которая отвечает на один вопрос: сколько товара и в какой кластер нужно привезти, чтобы не уйти в ноль в ближайшие недели."
  )
);
children.push(
  body(
    "Она не хранит статистику у себя. Каждый запуск программа заново забирает данные из Ozon Seller API, считает спрос по фактическим отгрузкам и складывает результат в Excel. Ни установки, ни базы данных, ни настроек — один файл."
  )
);
children.push(spacer(120));

/* три карточки */
const cardCell = (w, num, title, text) =>
  new TableCell({
    width: { size: w, type: WidthType.DXA },
    shading: { type: ShadingType.CLEAR, fill: PANEL, color: "auto" },
    margins: { top: 260, bottom: 280, left: 260, right: 240 },
    verticalAlign: VerticalAlign.TOP,
    children: [
      P([run(num, { size: 30, bold: true, color: "A8C8FF" })], { spacing: { before: 0, after: 120, line: 300 } }),
      P([run(title, { size: 16, bold: true, color: NAVY, characterSpacing: 30 })], {
        spacing: { before: 0, after: 100, line: 250 },
      }),
      P([run(text, { size: 17, color: MUTED })], { spacing: { before: 0, after: 0, line: 260 }, alignment: AlignmentType.LEFT }),
    ],
  });

const widths = [3132, 120, 3133, 120, 3133];
const cards = new Table({
  width: { size: CONTENT, type: WidthType.DXA },
  columnWidths: widths,
  layout: TableLayoutType.FIXED,
  borders: noBorders(),
  rows: [
    new TableRow({
      cantSplit: true,
      children: [
        cardCell(3132, "01", "СОБИРАЕТ", "Остатки на складах Ozon (FBO), остатки собственного склада (FBS) и историю отправлений за выбранный период."),
        new TableCell({ width: { size: 120, type: WidthType.DXA }, children: [P([run("")])] }),
        cardCell(3133, "02", "СЧИТАЕТ", "Среднесуточный спрос по каждому кластеру и целевую поставку на заданное число дней вперёд."),
        new TableCell({ width: { size: 120, type: WidthType.DXA }, children: [P([run("")])] }),
        cardCell(3133, "03", "ОТДАЁТ", "Excel-таблицу с фильтрами, подсветкой и статусами: что везти в первую очередь."),
      ],
    }),
  ],
});
children.push(cards);
children.push(spacer(200));

children.push(h2("Что нужно до старта"));
children.push(
  dataTable({
    headers: ["Что нужно", "Зачем это нужно"],
    widths: [2800, 6838],
    rows: [
      ["Windows 10 или новее", "Программа поставляется одним .exe и работает без установки."],
      ["Файл OzonPilot.exe", "Ни зависимостей, ни дополнительных библиотек — только этот файл."],
      ["Доступ в интернет", "Все данные берутся из Ozon Seller API в момент запуска."],
      ["Ключи Seller API", "Нужны права «Администратор» или «Аналитика»."],
    ],
  })
);
children.push(spacer(240));
children.push(
  callout("tip", "ДАННЫЕ ОСТАЮТСЯ У ВАС", [
    "Ключи и цифры никуда не отправляются, кроме api-seller.ozon.ru. Отчёты лежат локально в папке reports, а конфигурация — в вашем профиле Windows.",
  ])
);

/* 02 */
children.push(h1("02", "Первый запуск"));
children.push(
  body(
    "Ключи вводятся один раз. Дальше программа берёт их из конфигурационного файла, и запуск сводится к одной команде."
  )
);

children.push(step([run("Папка. ", { bold: true, color: NAVY }), run("Положите OzonPilot.exe в отдельную папку — например, "), mono("C:\\Ozon"), run(". Рядом программа будет создавать подпапку "), mono("reports"), run(" с готовыми отчётами.")]));
children.push(step([run("Запуск. ", { bold: true, color: NAVY }), run("Двойной клик по файлу или PowerShell. Из терминала удобнее: видно лог и понятнее ошибки, если они будут.")]));
children.push(codeBlock(["cd C:\\Ozon", ".\\OzonPilot.exe"]));
children.push(spacer(150));
children.push(step([run("Ключи. ", { bold: true, color: NAVY }), run("Программа попросит два значения: "), mono("Client-Id"), run(" — числовой идентификатор кабинета, и "), mono("Api-Key"), run(" — длинный буквенно-цифровой ключ. Вставляйте без пробелов и нажимайте Enter после каждого.")]));
children.push(step([run("Сохранение. ", { bold: true, color: NAVY }), run("Ключи проверяются реальным запросом к Ozon и сохраняются в "), mono("%APPDATA%\\CLIForOZON\\config.yaml"), run(". При следующих запусках вводить их не нужно. Сбросить и ввести заново: "), mono("-reset-keys"), run(".")]));
children.push(spacer(60));

children.push(
  callout("tip", "ГДЕ ВЗЯТЬ КЛЮЧИ", [
    "Личный кабинет Ozon Seller → Настройки → Seller API → Создать ключ. Нужны права «Администратор» или «Аналитика»: с урезанными правами API не отдаст остатки и историю продаж. Ключ показывается один раз — сохраните его сразу.",
  ])
);
children.push(spacer(160));
children.push(
  callout("warn", "ЕСЛИ OZON НЕ ПРИНЯЛ КЛЮЧИ", [
    "Ответ 401/403 означает, что ключ отозван, введён с опечаткой или у него нет нужных прав. Программа предложит ввести данные заново — всего до трёх попыток.",
  ])
);

/* 03 */
children.push(h1("03", "Обычный запуск"));
children.push(
  body(
    "Без флагов программа считает по настройкам из config.yaml, а если файла ещё нет — по значениям по умолчанию: запас на 25 дней, продажи за 28 дней, кластер Москва."
  )
);
children.push(codeBlock([".\\OzonPilot.exe"]));
children.push(spacer(200));

children.push(h2("Что происходит во время запуска"));
children.push(bullet("Проверка ключей и авторизация в Ozon Seller API."));
children.push(bullet("Загрузка каталога: артикулы и SKU всех товаров."));
children.push(bullet("Остатки FBO по кластерам — тот же источник, что раздел «Управление остатками» в кабинете."));
children.push(bullet("Остатки FBS — ваш собственный склад."));
children.push(bullet("История отправлений за окно -prodazhi: из неё получаются фактические продажи по кластерам."));
children.push(bullet("Отчёт о стоимости размещения: платное и бесплатное хранение."));
children.push(bullet("Расчёт поставки и запись Excel-файла в reports."));
children.push(spacer(140));
children.push(
  body(
    "Запросы идут последовательно и с паузами: Ozon ограничивает частоту обращений — примерно два запроса в секунду, а у статистики остатков отдельный жёсткий лимит. Поэтому на большом каталоге запуск занимает несколько минут: это нормально, прерывать его не нужно."
  )
);
children.push(spacer(100));
children.push(
  callout("warn", "ЛУЧШЕ ОШИБКА, ЧЕМ НУЛИ", [
    "Остатки, продажи и каталог должны загрузиться полностью. Если Ozon не отдал часть данных после повторов, программа остановится с ошибкой, а не подставит нули — иначе поставки получились бы завышенными.",
  ])
);

/* 04 */
children.push(h1("04", "Параметры"));
children.push(
  body("Все флаги необязательные. Флаг в командной строке всегда важнее значения из config.yaml.")
);
children.push(
  dataTable({
    headers: ["Флаг", "По умолчанию", "Что делает"],
    widths: [1850, 1700, 6088],
    monoCols: [0],
    align: ["left", "center", "left"],
    rows: [
      [{ text: "-postavka <дни>", mono: true }, { text: "25", align: "center" }, "На сколько дней вперёд обеспечить запас. Сверху программа добавляет 3 дня страхового запаса на логистику."],
      [{ text: "-prodazhi <дни>", mono: true }, { text: "28", align: "center" }, "Окно, за которое считаются реальные продажи."],
      [{ text: "-clusters <список>", mono: true }, { text: "Москва", size: 16 }, "Кластеры через запятую или all — все кластеры, где есть остатки."],
      [{ text: "-placement-file <путь>", mono: true }, { text: "—", align: "center" }, "Готовый отчёт о стоимости размещения (.xlsx или .csv) вместо запроса через API."],
      [{ text: "-file <путь>", mono: true }, { text: "—", align: "center" }, "Offline-режим: расчёт по локальному JSON, без обращений к Ozon."],
      [{ text: "-reset-keys", mono: true }, { text: "—", align: "center" }, "Забыть сохранённые ключи и запросить новые."],
      [{ text: "-debug", mono: true }, { text: "—", align: "center" }, "Подробные технические логи запросов: помогает понять, на каком шаге всё встало."],
    ],
  })
);
children.push(spacer(200));
children.push(
  body([
    "Поставка считается по формуле: ",
    run("среднесуточные продажи × (запас + 3 дня) − остаток FBO в кластере", { bold: true, color: NAVY }),
    ". Если результат отрицательный или нулевой, поставка не нужна. Значения по умолчанию для ",
    mono("-postavka"),
    ", ",
    mono("-prodazhi"),
    " и ",
    mono("-clusters"),
    " можно поменять в config.yaml: программа запоминает последние использованные кластеры.",
  ])
);

/* 05 */
children.push(h1("05", "Кластеры"));
children.push(
  body(
    "Кластер — это группа складов Ozon в одном регионе. Программа считает поставку отдельно по каждому кластеру, поэтому в отчёте на каждый из них приходится три колонки: продажи, остаток и поставка."
  )
);
children.push(
  dataTable({
    headers: ["Кластер", "Что писать в -clusters"],
    widths: [3000, 6638],
    rows: [
      ["Москва", "Москва — значение по умолчанию"],
      ["Санкт-Петербург", "Санкт-Петербург, СПб, Питер"],
      ["Поволжье", "Поволжье, Казань, Самара"],
      ["Юг", "Юг, Ростов, Ростов-на-Дону, Краснодар"],
      ["Урал", "Урал, Екатеринбург"],
      ["Сибирь", "Сибирь, Новосибирск"],
      ["Дальний Восток", "Дальний Восток, Хабаровск, Владивосток"],
      ["Воронеж", "Воронеж"],
      ["Калининград", "Калининград"],
      ["Северный Кавказ", "Северный Кавказ"],
      ["Беларусь", "Беларусь"],
      ["Казахстан", "Казахстан"],
    ],
  })
);
children.push(spacer(160));
children.push(
  body(
    "Названия, которые Ozon отдаёт в API — «Москва, МО и Дальние регионы», «Санкт-Петербург и СЗО» — тоже понимаются: программа приводит их к коротким именам."
  )
);
children.push(caption("НЕСКОЛЬКО КЛАСТЕРОВ СРАЗУ"));
children.push(codeBlock([".\\OzonPilot.exe -clusters \"Москва,Юг,Поволжье\""]));
children.push(spacer(160));
children.push(
  callout("tip", "ЕСЛИ НЕ ЗНАЕТЕ, ГДЕ ЕСТЬ ТОВАР", [
    "Запустите с -clusters all. Программа сама определит активные кластеры по остаткам FBO и посчитает только те, где товары действительно есть.",
  ])
);

/* 06 */
children.push(h1("06", "Примеры"));
children.push(body("Команды можно копировать целиком: используются только флаги из раздела 04."));

children.push(caption("СТАНДАРТНЫЙ РАСЧЁТ"));
children.push(codeBlock([".\\OzonPilot.exe"]));

children.push(caption("СВОЙ ГОРИЗОНТ И ВСЕ КЛАСТЕРЫ"));
children.push(codeBlock([".\\OzonPilot.exe -postavka 30 -prodazhi 14 -clusters all"]));

children.push(caption("ТРИ КЛАСТЕРА"));
children.push(codeBlock([".\\OzonPilot.exe -clusters \"Москва,Юг,Поволжье\""]));

children.push(caption("СО СВОИМ ФАЙЛОМ О РАЗМЕЩЕНИИ"));
children.push(codeBlock([".\\OzonPilot.exe -placement-file \"C:\\Users\\Иван\\Downloads\\отчёт.xlsx\""]));

children.push(caption("ПОСМОТРЕТЬ, ЧТО ПРОИСХОДИТ"));
children.push(codeBlock([".\\OzonPilot.exe -debug"]));

/* 07 */
children.push(h1("07", "Результат"));
children.push(
  body([
    "Файл создаётся заново при каждом запуске: ",
    mono("reports\\ozon_supply_2026-10-17_16-30-00.xlsx"),
    ". Папка ",
    mono("reports"),
    " появляется рядом с OzonPilot.exe автоматически.",
  ])
);

children.push(h2("Что внутри"));
children.push(bullet("Титульная строка со сводкой: дата, число позиций, платное хранение и сколько товаров закончилось совсем."));
children.push(bullet("Фильтры в заголовках; шапка не закреплена — таблица прокручивается целиком."));
children.push(bullet("Цветовые блоки по кластерам, акцент на «Поставке FBO» и подсветка «Размещения FBO»; лист «Легенда» расшифровывает цвета."));
children.push(spacer(100));

children.push(h2("Колонки"));
children.push(
  dataTable({
    headers: ["Колонка", "Что показывает"],
    widths: [2900, 6738],
    rows: [
      [{ text: "Артикул (OfferID)", bold: true, color: NAVY }, "Ваш артикул товара."],
      [{ text: "Остаток FBS", bold: true, color: NAVY }, "Сколько единиц на собственном складе продавца."],
      [{ text: "Размещение FBO", bold: true, color: NAVY }, "Платное или бесплатное хранение и дата окончания бесплатного периода."],
      [{ text: "Списано за хранение (₽)", bold: true, color: NAVY }, "Сумма хранения за период; выделяется красным, если больше нуля."],
      [{ text: "Продажи / Остаток / Поставка", bold: true, color: NAVY }, "Три колонки на каждый кластер: фактические продажи за период, текущий остаток FBO и рекомендованная поставка."],
    ],
  })
);
children.push(spacer(180));

children.push(h2("Статусы"));
children.push(
  dataTable({
    headers: ["Статус", "Что значит"],
    widths: [1900, 7738],
    monoCols: [0],
    rows: [
      [{ text: "OUT_OF_STOCK", mono: true, bold: true, color: "B42318", fill: "FDEBEC", size: 17 }, "Товара нет ни на FBO, ни на FBS, но спрос есть — это упущенная прибыль. Везти в первую очередь."],
      [{ text: "NEEDS_FBO", mono: true, bold: true, color: "B54708", fill: "FEF0E4", size: 17 }, "На складах Ozon дефицит: остатка не хватит до конца горизонта. Нужна поставка."],
      [{ text: "NEEDS_FBS", mono: true, bold: true, color: "8A6300", fill: "FFF7DF", size: 17 }, "Товар закончился на собственном складе продавца."],
      [{ text: "OK", mono: true, bold: true, color: "1B7F4B", fill: "E7F6EE", size: 17 }, "Запаса достаточно на всём горизонте."],
    ],
  })
);
children.push(spacer(180));
children.push(
  body(
    "Цвета не случайные: гамма привязана к кластеру, «Поставка FBO» выделена акцентом, а «Размещение FBO» подсвечивается по статусу хранения. Если что-то забылось — откройте лист «Легенда»."
  )
);

/* 08 */
children.push(h1("08", "Если что-то пошло не так"));
children.push(body("Сначала посмотрите последние строки лога: программа пишет, на каком шаге остановилась и почему."));
children.push(
  dataTable({
    headers: ["Ситуация", "Что делать"],
    widths: [3500, 6138],
    rows: [
      ["«Invalid keys! Ozon API denied access (401/403)»", { parts: ["Ключ отозван, введён с опечаткой или у него нет прав. Запустите ", mono(".\\OzonPilot.exe -reset-keys", { size: 17 }), " и введите ключи заново; проверьте права «Администратор» или «Аналитика»."] }],
      ["«Не удалось автоматически получить отчет о стоимости размещения…»", "Расчёт продолжится, но колонки о платном хранении будут пустыми. Повторите запуск позже или передайте файл вручную — флаг описан в разделе 04."],
      ["«критическая ошибка загрузки остатков FBO…»", "Ozon отдал не все данные. Подождите 10–15 минут и повторите: скорее всего, сработал лимит запросов. Если ошибка повторяется, проверьте, что ключ активен."],
      ["«Не удалось обновить файл конфигурации, работаю с текущими ключами»", { parts: ["Файл конфигурации защищён от записи. На расчёт это не влияет: ключи уже прочитаны. Чтобы убрать предупреждение, дайте программе права на папку ", mono("%APPDATA%\\CLIForOZON", { size: 17 }), "."] }],
      ["SmartScreen: «Windows защитила ваш компьютер»", "Файл без цифровой подписи. Нажмите «Подробнее» → «Выполнить в любом случае» или добавьте папку с программой в исключения."],
      ["Отчёта нет в папке reports", "Найдите в логе строку «Excel-отчет сохранен» — там указан полный путь. Если строки нет, расчёт не дошёл до конца; проверьте права на запись в папку с OzonPilot.exe."],
      ["Запуск идёт долго, в логе паузы", { parts: ["Это лимиты Ozon: программа ждёт между запросами и повторяет неудачные. Не закрывайте окно. Флаг ", mono("-debug", { size: 17 }), " покажет подробности."] }],
    ],
  })
);
children.push(spacer(260));
children.push(
  callout("tip", "ПРОВЕРКА ПЕРЕД ЗАПУСКОМ", [
    "Если меняли ключи или кабинет — начните с ",
    mono("-reset-keys"),
    ". Если первый раз считаете новый ассортимент — запустите с ",
    mono("-clusters all"),
    " и ",
    mono("-debug"),
    ": так видно и кластеры, и каждый шаг загрузки.",
  ])
);

/* подпись */
children.push(spacer(260));
children.push(
  new Paragraph({
    spacing: { before: 0, after: 0, line: 240 },
    border: { top: { style: BorderStyle.SINGLE, size: 4, color: BORDER, space: 10 } },
    children: [run("OzonPilot.exe  ·  инструкция для продавца  ·  версия 1.0", { size: 15, color: MUTED, characterSpacing: 20 })],
  })
);

/* ---------- колонтитулы ---------- */
const headerTable = new Table({
  width: { size: CONTENT, type: WidthType.DXA },
  columnWidths: [5600, 4038],
  layout: TableLayoutType.FIXED,
  borders: noBorders(),
  rows: [
    new TableRow({
      children: [
        new TableCell({
          width: { size: 5600, type: WidthType.DXA },
          margins: { top: 0, bottom: 120, left: 0, right: 0 },
          borders: { top: { ...NONE }, left: { ...NONE }, right: { ...NONE }, bottom: hair("E8EEF7") },
          children: [P([run("OZONPILOT.EXE", { size: 14, bold: true, color: MUTED, characterSpacing: 40 })], { spacing: { before: 0, after: 0, line: 220 } })],
        }),
        new TableCell({
          width: { size: 4038, type: WidthType.DXA },
          margins: { top: 0, bottom: 120, left: 0, right: 0 },
          borders: { top: { ...NONE }, left: { ...NONE }, right: { ...NONE }, bottom: hair("E8EEF7") },
          children: [
            P([run("РАСЧЁТ ПОСТАВОК НА OZON", { size: 14, color: "93A5B8", characterSpacing: 40 })], {
              alignment: AlignmentType.RIGHT,
              spacing: { before: 0, after: 0, line: 220 },
            }),
          ],
        }),
      ],
    }),
  ],
});

const footerTable = new Table({
  width: { size: CONTENT, type: WidthType.DXA },
  columnWidths: [5600, 4038],
  layout: TableLayoutType.FIXED,
  borders: noBorders(),
  rows: [
    new TableRow({
      children: [
        new TableCell({
          width: { size: 5600, type: WidthType.DXA },
          margins: { top: 120, bottom: 0, left: 0, right: 0 },
          borders: { top: hair("E8EEF7"), left: { ...NONE }, right: { ...NONE }, bottom: { ...NONE } },
          children: [P([run("Инструкция по работе с OzonPilot.exe", { size: 14, color: MUTED })], { spacing: { before: 0, after: 0, line: 220 } })],
        }),
        new TableCell({
          width: { size: 4038, type: WidthType.DXA },
          margins: { top: 120, bottom: 0, left: 0, right: 0 },
          borders: { top: hair("E8EEF7"), left: { ...NONE }, right: { ...NONE }, bottom: { ...NONE } },
          children: [
            P([new TextRun({ children: ["Стр. ", PageNumber.CURRENT, " из ", PageNumber.TOTAL_PAGES], size: 14, color: MUTED })], {
              alignment: AlignmentType.RIGHT,
              spacing: { before: 0, after: 0, line: 220 },
            }),
          ],
        }),
      ],
    }),
  ],
});

/* ---------- сборка ---------- */
const doc = new Document({
  creator: "CLIForOZON",
  title: "Инструкция по работе с OzonPilot.exe",
  subject: "Расчёт поставок на Ozon",
  description: "Краткая инструкция для продавца: первый запуск, параметры, кластеры, отчёт.",
  styles: {
    default: {
      document: {
        run: { font: "Segoe UI", size: 21, color: TEXT },
        paragraph: { spacing: { line: 300, after: 140 } },
      },
    },
    paragraphStyles: [
      {
        id: "Heading1",
        name: "heading 1",
        basedOn: "Normal",
        next: "Normal",
        quickFormat: true,
        paragraph: { outlineLevel: 0, spacing: { before: 520, after: 260 } },
        run: { size: 34, bold: true, color: NAVY, font: "Segoe UI" },
      },
      {
        id: "Heading2",
        name: "heading 2",
        basedOn: "Normal",
        next: "Normal",
        quickFormat: true,
        paragraph: { outlineLevel: 1, spacing: { before: 340, after: 140 } },
        run: { size: 24, bold: true, color: NAVY, font: "Segoe UI" },
      },
    ],
  },
  numbering: {
    config: [
      {
        reference: "bullets",
        levels: [
          {
            level: 0,
            format: LevelFormat.BULLET,
            text: "•",
            alignment: AlignmentType.LEFT,
            style: {
              paragraph: { indent: { left: 400, hanging: 260 } },
              run: { color: BLUE, size: 21 },
            },
          },
        ],
      },
      {
        reference: "steps",
        levels: [
          {
            level: 0,
            format: LevelFormat.DECIMAL,
            text: "%1.",
            alignment: AlignmentType.LEFT,
            style: {
              paragraph: { indent: { left: 460, hanging: 340 } },
              run: { bold: true, color: BLUE, size: 21 },
            },
          },
        ],
      },
    ],
  },
  sections: [
    {
      properties: {
        titlePage: true,
        page: {
          size: { width: PAGE_W, height: PAGE_H },
          margin: { top: 1134, right: 1134, bottom: 1134, left: 1134, header: 600, footer: 620 },
        },
      },
      headers: {
        default: new Header({ children: [headerTable] }),
        first: new Header({ children: [new Paragraph({ children: [] })] }),
      },
      footers: {
        default: new Footer({ children: [footerTable] }),
        first: new Footer({ children: [new Paragraph({ children: [] })] }),
      },
      children,
    },
  ],
});

const out = path.resolve(__dirname, "..", "Инструкция OzonPilot.docx");
Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync(out, buf);
  console.log("written:", out, buf.length, "bytes");
});


