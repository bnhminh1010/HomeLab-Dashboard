-- Older installations accepted any target below 100. Keep existing policy
-- rows inside the public 99.999% maximum before newer API validation reads
-- them, so an upgrade cannot make the SLO list unavailable.
UPDATE service_slo_policies
SET target_percent = 99.999
WHERE target_percent > 99.999;
