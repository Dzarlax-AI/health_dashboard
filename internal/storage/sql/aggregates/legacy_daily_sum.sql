SELECT day, source_total FROM (
    SELECT day, source_total,
           ROW_NUMBER() OVER (PARTITION BY day ORDER BY src_rank, source_total DESC) AS rn
    FROM (
        SELECT SUBSTRING(hour,1,10) AS day, source, SUM(avg_val) AS source_total,
               {{SUM_SOURCE_RANK}} AS src_rank
        FROM hourly_metrics
        WHERE metric_name = $1 {{FROM_CLAUSE}}
        GROUP BY SUBSTRING(hour,1,10), source
    ) sub
) ranked WHERE rn = 1
ORDER BY day
