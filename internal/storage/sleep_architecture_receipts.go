package storage

// Store each normalized day once per input revision, including verified empty
// days needed for windows. A window receipt references its exact 14 revisions.
const architectureReceiptSQL = `with items as (
	select item->>'day' as day,item,ordinal from jsonb_array_elements($1::jsonb) with ordinality j(item,ordinal)
), calendar as (
	select distinct day from items union
	select ($3::date+n)::text from generate_series(0,case when $5::date is not null then $4::date-$3::date else -1 end) n
), selected as (
	select c.day,jsonb_build_object('preferred',$2::text,'segments',
		coalesce(jsonb_agg(i.item order by i.ordinal) filter(where i.day is not null),'[]'::jsonb)) as inputs
	from calendar c left join items i using(day) group by c.day
), versions as (
	select *,encode(sha256(convert_to(jsonb_build_array(1,inputs)::text,'UTF8')),'hex') as hash from selected
), anchors as (
	select $5::date+n as anchor from generate_series(0,$6::date-$5::date) n
), decision_packets as (
	select 'sleep_architecture_day'::text metric,day decision_key,1 formula_version,inputs,'{}'::jsonb result from versions
	union all
	select 'sleep_architecture_window',anchor::text,1,
		jsonb_build_object('days',jsonb_object_agg(v.day,v.hash)),'{}'::jsonb
	from anchors a join versions v on v.day between (a.anchor-13)::text and a.anchor::text
	group by anchor having count(*)=14
)` + decisionJournalInsertSQL + " select count(*) from decision_packets"
