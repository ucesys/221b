-- A run's whole output is kept (DB-87): until now only the last 2 KiB of each host's
-- output was kept, in script_run_hosts.output_tail, and the whole output only in the
-- memory of the console that ran it or in the Toolbox's transcript volume, when there
-- was one. Each host's output is a row of script_run_outputs, written when the host's
-- part of the run ends: the newest 64 KiB of it (the engine keeps no more of a host's
-- output while it runs), is_cut set when what came before was not kept. It is masked as
-- the tail is: the values of secret parameters and of sensitive variables are blanked
-- as the script writes them, and a value made sensitive later is blanked here too.

CREATE TABLE script_run_outputs (
    script_run_host_id uuid PRIMARY KEY REFERENCES script_run_hosts (script_run_host_id) ON DELETE CASCADE,
    output text NOT NULL DEFAULT '',
    is_cut boolean NOT NULL DEFAULT false
);

COMMENT ON TABLE script_run_outputs IS 'The output of one host of a run, masked, its newest 64 KiB';
