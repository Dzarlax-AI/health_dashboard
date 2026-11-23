-- Run with the profile schema first in search_path. The invoker must have
-- SELECT on its own metric_points and observation_catalog relations.
CREATE OR REPLACE VIEW health_observation_v1 WITH (security_invoker=true) AS
SELECT c.metric_id AS observation_id,
       p.qty AS value,
       p.units,
       p.date AS original_date,
       p.source,
       p.origin,
       p.received_at,
       p.source_snapshot_at,
       c.catalog_units,
       c.catalog_grain,
       c.value_type
  FROM metric_points p
  JOIN observation_catalog c ON c.metric_id=p.metric_name;
