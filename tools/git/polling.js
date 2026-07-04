// Polling & Worktree helper functions
let localPollTimer = null;
let remotePollTimer = null;
let currentRunningLocalInterval = null;
let currentRunningRemoteInterval = null;
let currentRemoteEnabled = null;

function startPolling() {
    // In auto mode, refreshData() already armed the timers with the server-suggested
    // intervals. Reuse those running values if present so we don't tear them down and
    // briefly re-poll at the auto floor (30/90); fall back to the floor only
    // on cold start (before the first refresh sets a running interval).
    const localSec = gitConfig.local_poll_interval === 'auto'
        ? (currentRunningLocalInterval || 30) : gitConfig.local_poll_interval;
    const remoteSec = gitConfig.remote_poll_interval === 'auto'
        ? (currentRunningRemoteInterval || 90) : gitConfig.remote_poll_interval;
    adjustDynamicPolling(localSec, remoteSec);
}

function adjustDynamicPolling(targetLocal, targetRemote) {
    // Skip the origin-fetch timer when the repo has no remote (gitData.hasRemote
    // === false) — otherwise it would fire a guaranteed-to-fail `git fetch` every
    // cycle. Undefined (not yet known) still arms, to be conservative.
    const remoteEnabled = targetRemote > 0 && gitData.hasRemote !== false;
    // Re-arm on an interval change OR when the remote-enabled state flips, so a
    // hasRemote true→false transition with an unchanged interval still tears the
    // (now pointless) fetch timer down instead of being swallowed by the guard.
    if (targetLocal !== currentRunningLocalInterval
        || targetRemote !== currentRunningRemoteInterval
        || remoteEnabled !== currentRemoteEnabled) {
        currentRunningLocalInterval = targetLocal;
        currentRunningRemoteInterval = targetRemote;
        currentRemoteEnabled = remoteEnabled;

        stopPolling();

        if (targetLocal > 0) {
            localPollTimer = setInterval(() => {
                // Local poll: skip the suggestion (no extra git log). The remote
                // timer below refreshes the interval suggestion on its cadence.
                if (currentRepo) refreshData(false);
            }, targetLocal * 1000);
        }

        if (remoteEnabled) {
            remotePollTimer = setInterval(() => {
                if (currentRepo) {
                    fetch('/api/git/fetch', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ path: currentRepo })
                    }).then(r => r.json())
                      .then(data => {
                          if (data.ok) refreshData();
                      }).catch(e => console.error("Origin poll failed:", e));
                }
            }, targetRemote * 1000);
        }
    }
}

function stopPolling() {
    if (localPollTimer) clearInterval(localPollTimer);
    if (remotePollTimer) clearInterval(remotePollTimer);
    localPollTimer = null;
    remotePollTimer = null;
    // NOTE: currentRunningLocal/RemoteInterval and currentRemoteEnabled are owned
    // by adjustDynamicPolling.
    // Resetting them here defeats its "did the interval change?" guard, so every
    // refresh would tear down and recreate both timers — and because the remote
    // interval is longer than the local one, the remote (origin fetch) timer would
    // be cleared before it ever fires.
}

// Global UI listeners for polling auto checkboxes
document.getElementById('setting-local-poll-auto').addEventListener('change', (e) => {
    document.getElementById('setting-local-poll').disabled = e.target.checked;
});
document.getElementById('setting-remote-poll-auto').addEventListener('change', (e) => {
    document.getElementById('setting-remote-poll').disabled = e.target.checked;
});

