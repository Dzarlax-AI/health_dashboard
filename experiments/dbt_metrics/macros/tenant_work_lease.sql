{% macro tenant_work_lease() %}
{% if var('tenant_aggregate_worker',false) %}
    -- Held until the model transaction commits. A restart issuing a new lease
    -- waits for current writes, then rejects every later model of the orphan.
    do $lease$ begin
        perform 1 from {{ adapter.quote(target.schema) }}.hdbt_work_lease
        where singleton and attempt='{{ var("tenant_aggregate_attempt") }}'::uuid for share;
        if not found then raise exception 'obsolete aggregate work lease'; end if;
    end $lease$
{% endif %}
{% endmacro %}
