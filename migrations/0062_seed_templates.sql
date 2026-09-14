-- 0062_seed_templates: the three starter templates (phase 65).
--
-- Templates are global: they belong to no project and no tenant. scenario's
-- project_id column cannot be NULL, so a template carries 0 -- a value no
-- real project ever has (AUTO_INCREMENT starts at 1), which keeps them out of
-- every project-scoped query by construction. kind is 'portable' (the
-- fragments 0063 seeds are declarative Taurus requests) and engine stays ''
-- (portable scenarios pin no engine). created_by 'honryu' stamps the seeder,
-- mirroring the default project owner.
--
-- Seeded template_name slugs, and the load shape each template is meant to be
-- instantiated INTO (ramp and step shaping live in an execution's load
-- profile, which a clone does not carry -- only the workload does):
--
--   httpbin-baseline  "HTTPbin baseline"      GET /get; run it as a single
--                                             ramp-up with a 60s hold.
--   httpbin-delay     "HTTPbin latency probe" GET /delay/1; latency-focused,
--                                             keep concurrency modest.
--   httpbin-spike     "HTTPbin spike"         GET /get then POST /post; run it
--                                             as a 3-step pattern (low - high
--                                             - low) via the stage editor.
--
-- One statement per file is the migration convention, so the scenario rows
-- land here and their scenario_requests fragments in 0063, joined by
-- template_name rather than by id -- explicit ids would collide on any
-- database that already has scenarios.
--
-- INSERT IGNORE with a "no templates exist yet" guard, the same idempotency
-- stance 0049 takes: a second run of this file (a database seeded by hand, or
-- a re-applied backup) is a silent no-op rather than a duplicate or a
-- re-seed over operator deletions.
INSERT IGNORE INTO scenario (name, project_id, kind, engine, is_template, template_name, created_by)
SELECT v.name, 0, 'portable', '', TRUE, v.tname, 'honryu'
FROM (
    SELECT 'HTTPbin baseline' AS name, 'httpbin-baseline' AS tname
    UNION ALL
    SELECT 'HTTPbin latency probe', 'httpbin-delay'
    UNION ALL
    SELECT 'HTTPbin spike', 'httpbin-spike'
) AS v
WHERE NOT EXISTS (SELECT 1 FROM scenario WHERE is_template = TRUE);
