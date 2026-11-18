"""Standalone private readback of the local night cards, with no external assets."""
from datetime import datetime
from html import escape
from pathlib import Path
import json
import os
import tempfile
from zoneinfo import ZoneInfo

REASONS = {
    'episode_intervals': 'Интервалы основной ночи', 'same_source_day_summary': 'Дневная сумма того же источника',
    'unavailable': 'Данных недостаточно', 'continuous_recording': 'Непрерывная запись',
    'no_canonical_night': 'Запись основной ночи отсутствует', 'capture_incomplete': 'Неполная запись',
    'incomplete_capture': 'Неполная запись', 'duration_not_plausible': 'Длительность вне допустимого диапазона',
    'overlapping_stage_labels': 'Перекрытие разных стадий', 'interval_duration_mismatch': 'Интервалы не совпали с длительностью ночи',
    'no_same_source_stage_evidence': 'Нет стадий от выбранного источника', 'unknown_summary_quantity': 'Дневная сумма неполна',
    'day_duration_mismatch': 'Дневная сумма не совпала с длительностью', 'recorded_episode': 'Стадии совпали с основной ночью',
    'matched_day_summary': 'Дневная сумма согласуется с длительностью', 'daily_sum_has_no_timing': 'Дневная сумма без временных меток',
    'gaps_in_episode': 'Пропуски между интервалами', 'open_awake_boundary': 'Интервал бодрствования на границе записи',
    'coarse_sleep_only': 'Сон записан без детализации стадий', 'night_not_final_eligible': 'Предварительная запись',
    'missing_stage_components': 'Для оценки не хватает стадий или бодрствования', 'no_positive_duration': 'Нет положительной длительности',
    'low': 'Низкая уверенность', 'final': 'Полные входные данные', 'partial': 'Частичные данные', 'missing': 'Нет данных',
    'available': 'Норма рассчитана', 'insufficient_history': 'Недостаточно истории для нормы', 'no_night': 'Нет записи основной ночи',
    'ineligible_night': 'Ночь не подходит для сравнения', 'recorded': 'Бодрствование измерено',
    'inferred_zero_complete_stages': 'Для оценки принято 0 минут бодрствования по непрерывной записи стадий'
}

def label(value):
    return escape(REASONS.get(value, value or 'Нет данных'))

def number(value, suffix='', precision=1):
    if value is None:
        return '—'
    n = f'{value:.{precision}f}'.rstrip('0').rstrip('.') if precision else str(round(value))
    return n.replace('.', ',') + suffix

def local_time(value):
    if not value:
        return '—'
    t = datetime.fromisoformat(value.replace('Z','+00:00')).astimezone(ZoneInfo('Europe/Belgrade'))
    return t.strftime('%d.%m %H:%M %z')

def night_html(card, first, source_name):
    dur, stages, cont, quality = (card[k] for k in ('duration','stages','continuity','quality'))
    day = escape(card['wake_date'])
    stage_rows = []
    for key, name in [('deep','Глубокий'), ('rem','REM'), ('core','Лёгкий'), ('unspecified','Без детализации'), ('awake','Бодрствование')]:
        fraction = stages.get(key+'_fraction')
        share = number(fraction*100,'%') if fraction is not None else '—'
        stage_rows.append(f"<tr><th>{name}</th><td>{number(stages.get(key+'_minutes'))}</td><td>{share}</td></tr>")
    awaken = cont['awakening_count']
    count = number(awaken, precision=0)
    if awaken is None and cont.get('observed_internal_awake_bouts',0):
        count = 'не менее '+str(cont['observed_internal_awake_bouts'])
    delta = dur['delta_minutes']
    delta_text = f'{delta:+d} мин' if delta is not None else '—'
    quality_score = number(quality.get('score_pct'), ' из 100', precision=0)
    source = escape(str(source_name or card.get('source') or '—'))
    return f"""<details {'open' if first else ''}><summary><time>{day}</time> · {number(dur['duration_minutes'],' мин',0)} сна · качество {quality_score}</summary>
<div><h3>Длительность и личная норма</h3><p>{label(dur['status'])}. Норма {number(dur['reference_minutes'],' мин',0)}, разница {delta_text}. Сопоставимых ночей {number(dur['eligible_nights'],precision=0)}.</p>
<h3>Записанные стадии</h3><p>{label(stages['stage_basis'])}. {label(stages['stage_reason'])}.</p><table><thead><tr><th>Стадия</th><th>Минуты</th><th>Доля сна</th></tr></thead><tbody>{''.join(stage_rows)}</tbody></table>
<h3>Непрерывность</h3><p>Внутренние пробуждения — {count}. {label(cont['continuity_reason'])}. Пропуски {number(cont['gap_minutes'],' мин')}. Границы записи {local_time(cont['episode_start'])} — {local_time(cont['episode_end'])}.</p>
<h3>Оценка качества</h3><p>{quality_score}. {label(quality['confidence'])}. {label(card['quality_basis'])}. {label(card['quality_awake_basis'])}.</p><p>Компоненты оценки — длительность {number(quality.get('duration_pct') if quality['confidence']!='missing' else None,'%',0)}, непрерывность {number(quality.get('continuity_pct'),'%',0)}, структура {number(quality.get('structure_pct'),'%',0)}.</p>
<small>Источник в снимке <code>{source}</code></small></div></details>"""

def write_night_cards(cur, output: Path, reconciled_replay=None):
    cur.execute('select card from semantic_sleep_night_card_v1 order by wake_date desc')
    cards=[r[0] for r in cur.fetchall()]
    names = {}
    if reconciled_replay:
        # The loader has already reconciled the dated source and epoch. Names
        # are used only in the private owner-readable HTML, never in aggregates.
        with reconciled_replay.open() as packets:
            for line in packets:
                item = json.loads(line)
                if item['kind'] == 'night':
                    names[item['day']] = item['source']
    sections=''.join(night_html(c,i==0,names.get(c['wake_date'])) for i,c in enumerate(cards))
    doc=f"""<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Основная ночь — локальная проверка</title>
<style>body{{margin:0;background:#f4f7f6;color:#172d31;font:16px/1.5 system-ui,sans-serif}}main{{max-width:48rem;margin:auto;padding:clamp(1rem,4vw,2.5rem)}}details{{background:white;border:1px solid #b5c9c3;border-radius:8px;margin:8px 0}}details div,summary{{padding:12px 16px}}summary{{cursor:pointer;font-weight:650}}details[open] summary{{background:#e8f2ee}}h3{{margin:.8em 0 .2em}}p{{margin:.4em 0}}table{{width:100%;border-collapse:collapse}}td,th{{padding:3px 8px;text-align:right;border-bottom:1px solid #d6e2dc}}th:first-child{{text-align:left}}code{{overflow-wrap:anywhere}}</style>
<main><h1>Основная ночь</h1><p>{len(cards)} дат · {escape(cards[-1]['wake_date'])} — {escape(cards[0]['wake_date'])} · Europe/Belgrade</p>
<p>Выберите дату. Прочерк означает, что значение установить не удалось. Ноль показан отдельно. Оценка рассчитана по формуле приложения на доступных данных основной ночи.</p>{sections}</main></html>"""
    with tempfile.NamedTemporaryFile(mode='w',dir=output.parent,prefix='.night-cards-',delete=False,encoding='utf-8') as f:
        f.write(doc)
    os.replace(f.name,output)
