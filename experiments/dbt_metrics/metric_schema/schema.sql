-- Definition and fact tables for an empty, owner-controlled profile schema.
-- profile_sql.py installs these tables and the metric_read_v1 projection together.
-- Keep the existing tenant boundary: each reader receives access to one schema.
create table metric_definition_v1 (
    metric_id text primary key,
    definition jsonb not null check (jsonb_typeof(definition) = 'object'),
    disposition text not null check (disposition in ('active','input_contract','unclassified','dormant')),
    reason text,
    read_sources jsonb not null,
    dbt_binding jsonb,
    value_type text generated always as (definition->>'value_type') stored not null,
    unique (metric_id, value_type),
    check ((disposition = 'active') = (reason is null))
);

create table metric_value_v1 (
    metric_id text not null,
    value_type text not null,
    source text not null,
    sample_key jsonb not null check (jsonb_typeof(sample_key) = 'object' and sample_key <> '{}'),
    value jsonb,
    source_metadata jsonb not null default '{}'::jsonb,
    foreign key (metric_id,value_type) references metric_definition_v1(metric_id,value_type),
    check (value is null or case value_type
        when 'number' then jsonb_typeof(value)='number'
        when 'integer' then case when jsonb_typeof(value)='number'
            then trunc((value #>> '{}')::numeric)=(value #>> '{}')::numeric else false end
        when 'duration' then jsonb_typeof(value)='number'
        when 'text' then jsonb_typeof(value)='string'
        when 'timestamp' then jsonb_typeof(value)='string'
        when 'boolean' then jsonb_typeof(value)='boolean'
        when 'text_list' then jsonb_typeof(value)='array'
        when 'count_map' then jsonb_typeof(value)='object'
        when 'number_map' then jsonb_typeof(value)='object'
        when 'json' then true else false end),
    primary key (metric_id, source, sample_key)
);
