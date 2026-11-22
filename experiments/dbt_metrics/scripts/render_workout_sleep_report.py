"""Render one aggregate study result from stdin as a self-contained Russian HTML report."""

import argparse
import json
import sys
from datetime import date
from decimal import Decimal, ROUND_HALF_UP
from html import escape


def minutes(hours):
    return int((Decimal(str(hours)) * 60).quantize(Decimal("1"), rounding=ROUND_HALF_UP))


def duration(total):
    hours, mins = divmod(abs(total), 60)
    return (f"{hours} ч " if hours else "") + f"{mins} мин"


def signed(hours):
    value = minutes(hours)
    return ("+" if value > 0 else "−" if value < 0 else "") + duration(value)


def result(epoch, multiple):
    title = f'<h3>Период источника {int(epoch["epoch"])}</h3>' if multiple else ""
    positive, meh, sick = (int(epoch[key]) for key in ("positive", "meh", "sick"))
    counts = f'Хорошо или отлично — {positive} · Не очень — {meh} · Болезнь — {sick}'
    if epoch["median_diff_hours"] is None:
        return f'<article>{title}<h2>Пока мало утр для сравнения</h2><p>{counts}.</p><p>Для сравнения нужны хотя бы пять ответов в каждой из первых двух групп за один период источника данных.</p></article>'

    gap = epoch["median_diff_hours"]
    value = minutes(gap)
    if value:
        direction = "больше" if value > 0 else "меньше"
        finding = f'Медианный интервал от конца тренировки до сна перед утрами с хорошей оценкой был на <strong>{duration(value)} {direction}</strong>, чем перед утрами с ответом «не очень».'
    else:
        finding = 'Типичные интервалы в обеих группах совпали с точностью до минуты.'
    rows = "".join(
        f'<tr><th scope="row">{label}</th><td>{count}</td><td>{duration(minutes(epoch[key]))}</td></tr>'
        for label, count, key in (
            ("Хорошо или отлично", positive, "median_positive_hours"),
            ("Не очень", meh, "median_meh_hours"),
        )
    )
    if epoch.get("loo_count"):
        sensitivity = (f'По очереди убрали каждое утро с ответом «не очень» и повторили сравнение. '
                       f'Число повторов — {int(epoch["loo_count"])}. Разница медиан составила от '
                       f'{signed(epoch["loo_median_min_hours"])} до {signed(epoch["loo_median_max_hours"])}. '
                       f'Для средних значений диапазон был от {signed(epoch["loo_mean_min_hours"])} '
                       f'до {signed(epoch["loo_mean_max_hours"])}.')
    else:
        sensitivity = 'После исключения одного ответа останется слишком мало утр для повторной проверки.'
    return f'''<article>{title}<p class="finding">{finding}</p>
<table><caption>От тренировки до предполагаемого начала сна</caption>
<thead><tr><th scope="col">Самочувствие утром</th><th scope="col">Утр</th><th scope="col">Медиана</th></tr></thead><tbody>{rows}</tbody></table>
<p class="note">Медиана — середина ряда, если упорядочить интервалы по длине. Ответы о болезни ({sick}) в сравнение не вошли.</p>
<details><summary>Насколько устойчив результат?</summary><p>{sensitivity}</p><p>Знак «+» означает, что перед утрами с хорошей оценкой интервал был длиннее.</p></details></article>'''


STYLE = """
*{box-sizing:border-box}body{margin:0;background:#f5f4ee;color:#182e29;font:17px/1.6 system-ui,sans-serif}
main{max-width:780px;margin:auto;padding:48px 24px 70px}header{border-bottom:2px solid #365e51;padding-bottom:22px}
h1,h2,h3,p{margin:0 0 .75em}h1,.finding{font-family:Georgia,serif;line-height:1.18;font-weight:500}
h1{font-size:clamp(32px,5vw,50px)}h2{font-size:1.35rem}article{background:#fffefa;border:1px solid #c6d2c7;border-radius:12px;padding:24px;margin:24px 0}
.finding{font-size:clamp(24px,4vw,34px)}strong{color:#17674d}.note,header p,footer{color:#475f54;font-size:.92rem}
table{width:100%;border-collapse:collapse;margin:1.3em 0 .6em}caption,th{text-align:left}caption{font-weight:650;padding-bottom:8px}
td,th{padding:8px 5px;border-bottom:1px solid #dce1d9}td{text-align:right;font-variant-numeric:tabular-nums}thead th:not(:first-child){text-align:right}
summary{cursor:pointer;font-weight:650;padding:10px 0}details{border-top:1px solid #c6d2c7}summary:focus-visible{outline:2px solid #17674d}
li{margin:.4em 0}footer{margin-top:24px;overflow-wrap:anywhere}@media(max-width:500px){main{padding:24px 14px}article{padding:16px}td,th{padding:6px 3px}}
"""


def render(data, *, synthetic=False):
    heading = "Health / Пример на вымышленных данных" if synthetic else "Health / Исследование"
    start, end = (date.fromisoformat(data[key]).strftime("%d.%m.%Y") for key in ("start_date", "end_date"))
    epochs = data["epochs"]
    findings = "".join(result(e, len(epochs) > 1) for e in epochs)
    if not findings:
        findings = '<article><h2>Пока мало утр для сравнения</h2><p>За этот период не нашлось ночей, для которых удалось сопоставить тренировку, сон и утренний ответ.</p></article>'
    return f'''<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Health · Тренировка, сон и самочувствие</title><style>{STYLE}</style><main><header><p>{heading} · {start} — {end}</p>
<h1>Как отличалось время от тренировки до сна в зависимости от самочувствия следующим утром?</h1></header>
{findings}<details><summary>Почему? Как отбирались ночи</summary><ol>
<li>Календарных дней — {int(data["calendar_days"])}. Ночей с полной и пригодной записью длительности — {int(data["eligible_nights"])}.</li>
<li>Начало сна оценивали по стадиям. Число дат с оценкой хотя бы одним из двух способов — {int(data["union_onsets"])}.</li>
<li>Выбирали тренировку с самым поздним окончанием среди начатых до сна. Если она ещё шла при засыпании или закончилась более суток назад, ночь исключали. Для сравнения нужен ответ после сна между 05:00 и 12:00 по местному времени, до истечения срока ответа. Подходящая тренировка нашлась для {int(data["union_onset_prior_workout"])} дат, утренний ответ — для {int(data["union_onset_morning_answer"])}. Все три условия совпали для следующего числа ночей — {int(data["usable_paired_dates"])}.</li></ol>
<p>Один способ использует сохранённую версию стадий, другой — стадии в пакете устройства. Если оба способа находят сон, проверяем согласие начала и конца в пределах минуты и совпадение отобранной пары. Каждая дата учитывается один раз.</p>
<p>В итог входят также старые пакеты, для которых удалось проверить совпадение длительности и версии покрытия. Эти пакеты могли поступить при повторной синхронизации. Ответы сравниваем внутри периода источника устройства, назначенного записи ночи. В старых оценках стадии могут относиться к другому источнику или периоду; прямой связи отдельных стадий с основным эпизодом сна нет, поэтому время засыпания приблизительно.</p><p class="note">Версия расчёта · {escape(str(data["method_version"]))}</p></details>
<p class="note">Это наблюдение по истории одного профиля. По нему нельзя установить, изменилось бы самочувствие при переносе тренировки.</p>
<footer>Часовой пояс · {escape(str(data["timezone"]))}</footer></main></html>'''


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--synthetic", action="store_true", help="label a synthetic example")
    args = parser.parse_args()
    print(render(json.load(sys.stdin), synthetic=args.synthetic))
