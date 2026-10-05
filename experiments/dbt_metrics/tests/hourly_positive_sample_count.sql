SELECT * FROM {{ ref('hourly_metrics') }} WHERE sample_count <= 0
