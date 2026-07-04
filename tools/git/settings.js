// Settings UI
const settingsPanel = document.getElementById('settings-panel');
document.getElementById('btn-settings').addEventListener('click', (e) => {
    const btn = e.currentTarget;
    const open = settingsPanel.classList.toggle('open');
    btn.classList.toggle('active', open);
    if(open) {
        closeAddRepoPanel();
        document.getElementById('setting-default-repo').value = gitConfig.default_repo;
        document.getElementById('setting-log-limit').value = gitConfig.log_limit;
        const localAuto = gitConfig.local_poll_interval === 'auto';
        document.getElementById('setting-local-poll-auto').checked = localAuto;
        document.getElementById('setting-local-poll').value = localAuto ? 30 : gitConfig.local_poll_interval;
        document.getElementById('setting-local-poll').disabled = localAuto;

        const remoteAuto = gitConfig.remote_poll_interval === 'auto';
        document.getElementById('setting-remote-poll-auto').checked = remoteAuto;
        document.getElementById('setting-remote-poll').value = remoteAuto ? 90 : gitConfig.remote_poll_interval;
        document.getElementById('setting-remote-poll').disabled = remoteAuto;

        document.getElementById('setting-show-log').checked = gitConfig.layout.show_log;
        document.getElementById('setting-commit-template').value = gitConfig.commit_template;

        const kbCont = document.getElementById('setting-keybindings-container');
        kbCont.innerHTML = '<div class="settings-title">Keybindings</div>';
        for(const [key, val] of Object.entries(gitConfig.keybindings)) {
            const row = document.createElement('div'); row.className = 'setting-row';
            const lbl = document.createElement('label'); lbl.textContent = key + ':'; lbl.style.width = '80px';
            const inp = document.createElement('input'); inp.type = 'text'; inp.value = val; inp.style.width = '40px'; inp.maxLength = 1;
            inp.onchange = (e) => { gitConfig.keybindings[key] = e.target.value; };
            row.appendChild(lbl); row.appendChild(inp);
            kbCont.appendChild(row);
        }
    }
});

// Parse a manual poll-interval field, flooring empty/invalid/non-positive input
// to a sane default instead of 0 (which would silently disable that timer).
function parsePollInterval(value, fallback) {
    const n = parseInt(value, 10);
    return Number.isFinite(n) && n >= 1 ? n : fallback;
}

document.getElementById('btn-save-settings').addEventListener('click', () => {
    gitConfig.default_repo = document.getElementById('setting-default-repo').value;
    gitConfig.log_limit = parseInt(document.getElementById('setting-log-limit').value) || 100;

    const localAuto = document.getElementById('setting-local-poll-auto').checked;
    gitConfig.local_poll_interval = localAuto ? 'auto' : parsePollInterval(document.getElementById('setting-local-poll').value, 30);

    const remoteAuto = document.getElementById('setting-remote-poll-auto').checked;
    gitConfig.remote_poll_interval = remoteAuto ? 'auto' : parsePollInterval(document.getElementById('setting-remote-poll').value, 90);
    
    gitConfig.layout.show_log = document.getElementById('setting-show-log').checked;
    gitConfig.commit_template = document.getElementById('setting-commit-template').value;
    saveSettings();
    settingsPanel.classList.remove('open');
    document.getElementById('btn-settings').classList.remove('active');
    refreshData();
    startPolling();
});

function updateFooterHints() {
    const hints = [];
    const k = gitConfig.keybindings;
    hints.push(`1-5: Tabs`);
    hints.push(`${k.refresh}: Refresh`);
    if(activeTab === 'status') {
        hints.push(`${k.stage}: Stage/Unstage`);
        hints.push(`${k.commit}: Focus Commit`);
    } else if (activeTab === 'branches') {
        hints.push(`Enter: Checkout`);
        hints.push(`${k.new_branch}: New Branch`);
    } else if (activeTab === 'stash') {
        hints.push(`Space: Pop`);
        hints.push(`d: Drop`);
    }
    hints.push(`${k.push}: Push`);
    hints.push(`${k.pull}: Pull`);

    document.getElementById('footer').textContent = hints.join('  |  ');
}

