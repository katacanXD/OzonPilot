@echo off
chcp 65001 > nul
echo Запуск анализа поставок OZON...
OzonPilot.exe -postavka 25 -prodazhi 30 -placement-file "C:\Users\Katacan\Downloads\Telegram Desktop\2026_10_18_Стоимость_размещения_по_товарам_на_складе_Ozon.xlsx" -clusters "Москва,Питер"
echo.
pause