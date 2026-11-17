CREATE INDEX spans_run_id ON spans (json_extract(resource_attributes, '$."geko.run.id"'))
WHERE json_extract(resource_attributes, '$."geko.run.id"') IS NOT NULL;
