-- Cancellation revokes only an unapplied frozen proposal. Old binaries already
-- reject every status other than ready, so cancelled proposals remain fenced.
ALTER TABLE ai_ops_proposals DROP CONSTRAINT ai_ops_proposals_status_check;
ALTER TABLE ai_ops_proposals ADD CONSTRAINT ai_ops_proposals_status_check
 CHECK(status IN ('ready','blocked','applied','expired','conflict','cancelled'));
