package gaterunner

// Column names follow nicos-commit-delivery/internal/runnerlive/store.go.
// Per-host limits prevent a busy runner's history from hiding its peers.
const liveSnapshotSQL = `
WITH jobs AS (
 SELECT *, row_number() OVER (PARTITION BY host ORDER BY sample_ts_unix DESC) AS n
 FROM runner_jobs_current WHERE state NOT IN ('TERMINAL','KILLED') AND phase != 'terminal'
), runs AS (
 SELECT *, row_number() OVER (PARTITION BY host ORDER BY ts_unix DESC) AS n FROM runner_runs
), kills AS (
 SELECT *, row_number() OVER (PARTITION BY host ORDER BY killed_ts_unix DESC) AS n FROM runner_kills
)
SELECT 'host' AS kind, json_object(
 'host',host,'runner_version',runner_version,'load1',load1,'disk_free_gb',disk_free_gb,
 'cache_gb',build_cache_gb,'last_poll_at',CASE WHEN last_poll_ok_unix > 0 THEN strftime('%Y-%m-%dT%H:%M:%SZ',last_poll_ok_unix,'unixepoch') END,
 'poll_error',nullif(poll_error,'')) AS payload FROM runner_hosts
UNION ALL SELECT 'job', json_object(
 'host',host,'job_id',job_id,'delivery_id',delivery_id,'repo',repo,'sha',sha,'class',class,
 'phase',phase,'state',state,'verdict',CASE WHEN phase='running' THEN state ELSE '' END,
 'enqueued_at',enqueued_at,'started_at',started_at,'updated_at',sample_ts,
 'elapsed_s',elapsed_s,'queue_wait_s',queue_wait_s,'last_output_age_s',last_output_age_s,
 'cpu_pct',cpu_pct,'rss_mb',rss_mb,'exit_code',exit,'detail',detail) FROM jobs WHERE n <= 50
UNION ALL SELECT 'run', json_object(
 'host',host,'job_id',job_id,'delivery_id',delivery_id,'repo',repo,'sha',sha,'class',class,
 'phase','terminal','state',CASE WHEN exit=0 THEN 'passed' WHEN exit IS NOT NULL THEN 'failed' ELSE 'unknown' END,
 'finished_at',ts,'elapsed_s',dur_s,'exit_code',exit,'detail',reason) FROM runs WHERE n <= 20
UNION ALL SELECT 'kill', json_object(
 'host',host,'job_id',job_id,'delivery_id',delivery_id,'class',class,'finished_at',killed_ts,
 'elapsed_s',elapsed_s,'last_output_age_s',last_output_age_s,'cpu_pct',cpu_pct,'detail',detail)
 FROM kills WHERE n <= 20;
`
