package main

import (
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"

	"health-receiver/internal/storage"
)

type napEnergyPreview struct {
	Message    string
	BankBefore *int
	BankAfter  int
	Points     string
}

var napEnergyPage = template.Must(template.New("napEnergy").Parse(`<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>EnergyBank и дневной сон</title>
<style>
  body { margin: 0; background: #f3f5f7; color: #18252d; font: 1rem/1.55 system-ui, sans-serif; }
  main { max-width: 40rem; margin: auto; padding: clamp(1rem, 5vw, 3rem); }
  h1 { font-size: clamp(1.6rem, 5vw, 2.25rem); line-height: 1.15; margin-block: .25rem .75rem; }
  p { margin-block: .5rem; }
  section { background: #fff; border: 1px solid #ccd5db; border-radius: .75rem; padding: 1.25rem; margin-block: 1.25rem; }
  h2 { font-size: 1.2rem; margin: 0 0 .5rem; }
  .note { color: #42555f; }
  .score { font-variant-numeric: tabular-nums; font-weight: 650; }
</style>
</head>
<body><main>
<p class="note">Пилот · расчётная прибавка</p>
<h1>EnergyBank</h1>
<p>Оценка запаса энергии с учётом сна и нагрузки.</p>
<section aria-labelledby="nap-heading">
  <h2 id="nap-heading">Вклад дневного сна</h2>
  <p>{{.Message}}</p>
  {{if .BankBefore}}<p class="score">Банк без прибавки {{.BankBefore}} · с прибавкой {{.BankAfter}}</p>{{end}}
  {{if .Points}}<p class="note">Вклад до округления и ограничения шкалы — {{.Points}}.</p>{{end}}
</section>
</main></body></html>`))

func renderNapEnergyPreview(w io.Writer, result storage.EnergyNapShadowResult) error {
	view := napEnergyPreview{}
	switch result.Status {
	case "source_available_after_window":
		view.Message = "Подтверждение сна стало доступно после окончания окна восстановления. Прибавку к банку не начислили."
	case "base_stale":
		view.Message = "Данных для оценки банка пока недостаточно. Прибавку к баллу сейчас нельзя оценить."
	case "selection_unavailable_at_time":
		view.Message = "К этому моменту данные об эпизоде ещё не были доступны расчёту."
	case "compared":
		if result.Gradual != nil && result.DisplayBefore != nil {
			view = explainNapEnergyCredit(*result.Gradual, result.DisplayBefore)
		}
	}
	if view.Message == "" {
		view = napEnergyPreview{Message: "Пока недостаточно данных об эпизоде для расчёта прибавки."}
	}
	return napEnergyPage.Execute(w, view)
}

func explainNapEnergyCredit(variant storage.EnergyNapShadowVariant, before *int) napEnergyPreview {
	view := napEnergyPreview{BankBefore: before, BankAfter: variant.DisplayAfter}
	if points := variant.PointsBeforeLimit; points > 0 {
		if points < 0.05 {
			view.Points = "меньше 0,1 балла"
		} else {
			view.Points = strings.ReplaceAll(strconv.FormatFloat(points, 'f', 1, 64), ".", ",") + " балла"
		}
	}
	switch variant.Reason {
	case storage.NapNoActiveCredit:
		view.Message = "Сейчас в расчёте банка нет прибавки за дневной сон."
	case storage.NapCreditRamping:
		view.Message = "Дневной сон учтён. Прибавка начинает расти после пробуждения."
	case storage.NapCreditShown:
		view.Message = fmt.Sprintf("Дневной сон повысил показание банка на %s.", napDisplayPoints(variant.VisibleDelta))
	case storage.NapCreditCapped:
		view.Message = fmt.Sprintf("Дневной сон повысил показание банка на %s. Банк достиг максимума.", napDisplayPoints(variant.VisibleDelta))
	case storage.NapAtCeiling:
		view.Message = "Дневной сон учтён. На экране уже максимальное значение."
	case storage.NapBelowDisplayFloor:
		view.Message = "Дневной сон улучшил расчётный запас. На экране ещё 0."
	case storage.NapCreditRoundedAway:
		view.Message = "Небольшая прибавка учтена. После округления показание не изменилось."
	}
	return view
}

func napDisplayPoints(points int) string {
	unit := "баллов"
	if points%100 < 11 || points%100 > 14 {
		switch points % 10 {
		case 1:
			unit = "балл"
		case 2, 3, 4:
			unit = "балла"
		}
	}
	return fmt.Sprintf("%d %s", points, unit)
}
