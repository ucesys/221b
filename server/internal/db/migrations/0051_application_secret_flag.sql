-- Whether an application has a Secret (decided by the owner on November 22, 2026):
-- the console makes the Secret <release>-app-<id> only for an application with
-- something secret, values, parameters, commands' parameters or a repository token,
-- so a Secret that is not there is told apart from one that was never needed. True
-- for the rows already there: until this step every application had a Secret.
ALTER TABLE applications ADD COLUMN is_secret_set boolean NOT NULL DEFAULT true;
