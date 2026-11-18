"""Verify the compiled catalog and the native nightly metrics on the shadow target."""
from psycopg2 import sql

from check_semantic_catalog import definitions, read_catalog


def verify_dbt_catalog(cur, manifest):
    catalog = read_catalog()
    registry = manifest['nodes']['model.health_dbt_metrics_pilot.semantic_metric_catalog_v1']
    if registry['config']['meta']['health_catalog'] != catalog:
        raise ValueError('dbt manifest and source metric catalog differ')
    expected = {item['metric_id']: item for item in definitions(catalog)}
    cur.execute('select metric_id, definition, dbt_binding from semantic_metric_catalog_v1')
    actual = {}
    bindings = {}
    for key, definition, binding in cur:
        actual[key] = dict(definition, metric_id=key)
        if binding is not None:
            bindings[key] = binding
    if actual != expected or bindings != catalog['dbt_bindings']:
        raise ValueError('dbt catalog rows differ from resolved source definitions or bindings')

    checked_metrics = []
    for metric in manifest['metrics'].values():
        params = metric['type_params']
        aggregation = params['metric_aggregation_params']
        if not aggregation or aggregation['semantic_model'] != 'main_night':
            continue
        key = metric['config']['meta']['catalog_id']
        column = params['expr']
        binding = bindings[key]
        if aggregation['agg'] != 'average' or column not in binding['columns']:
            raise ValueError(f"{metric['name']}: unexpected column or aggregation for {key}")
        # A missing canonical night can still have a dated calendar row.
        # Compare NULL as well as measured values across all those dates.
        query = sql.SQL('''select count(*), count(*) filter (
                  where m.{column} is distinct from b.{column})
            from semantic_main_night_metrics_v1 m left join {source} b
                on b.wake_date=m.night_id''').format(
                    column=sql.Identifier(column), source=sql.Identifier(binding['model']))
        cur.execute(query)
        dates, differences = cur.fetchone()
        if not dates or differences:
            raise ValueError(f"{metric['name']}: expected dated values from {binding['model']}")
        checked_metrics.append(metric['name'])
    if not checked_metrics:
        raise ValueError('main-night semantic metrics missing from dbt manifest')
    return {'definitions': len(actual), 'model_bindings': len(bindings), 'metrics': sorted(checked_metrics)}
