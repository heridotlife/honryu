-- 0061_scenario_templates: the template flag on a scenario (phase 65).
--
-- A template is a scenario used as a starting point rather than a runnable
-- test: GET /api/templates lists only rows with is_template, the per-project
-- scenario paths hide them, and POST /api/scenarios/{id}/instantiate clones a
-- template into a fresh is_template=false scenario. Deliberately a flag on the
-- existing table, not a new table: a template IS a scenario (same kind, same
-- requests fragment in scenario_requests), and instantiate copies rows with
-- the code paths CreateScenario/SetRequests already provide.
--
-- is_template defaults false, so every existing row stays an ordinary
-- scenario and no backfill is needed. template_name is the template's stable
-- slug ("httpbin-baseline"); NULL for an ordinary scenario, required for a
-- template (mirrored in domain/scenario's MaxTemplateNameLen). No index: the
-- catalog is tiny and global, and the seed/instantiate joins below resolve a
-- handful of rows once.
ALTER TABLE scenario
    ADD COLUMN is_template BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN template_name VARCHAR(128) NULL;
