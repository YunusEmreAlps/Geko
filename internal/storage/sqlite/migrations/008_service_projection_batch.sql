-- Ingest updates trace_services once per trace, namespace and service in each
-- export instead of once per span. Deletes still adjust counts per span.
DROP TRIGGER spans_project_service_insert;
