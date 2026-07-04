// Interactions — custom repo switcher (search / reorder / hide)
const repoSwitcher = document.getElementById('repo-switcher');

function openRepoSwitcher() {
    // Close the other header panels so only one is open at a time.
    document.getElementById('settings-panel').classList.remove('open');
    document.getElementById('btn-settings').classList.remove('active');
    closeAddRepoPanel();
    repoSwitcher.classList.add('open');
    renderRepoSwitcher();
    setTimeout(() => {
        const s = document.getElementById('repo-search');
        s.focus(); s.select();
    }, 30);
}

function closeRepoSwitcher() {
    repoSwitcher.classList.remove('open');
}

document.getElementById('repo-switcher-btn').addEventListener('click', (e) => {
    e.stopPropagation();
    if (repoSwitcher.classList.contains('open')) closeRepoSwitcher();
    else openRepoSwitcher();
});

// Close when clicking outside the switcher.
document.addEventListener('click', (e) => {
    if (repoSwitcher.classList.contains('open') && !repoSwitcher.contains(e.target)) {
        closeRepoSwitcher();
    }
});

document.getElementById('repo-search').addEventListener('input', () => renderRepoSwitcher());
document.getElementById('repo-search').addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
        e.stopPropagation();
        if (e.target.value) { e.target.value = ''; renderRepoSwitcher(); }
        else closeRepoSwitcher();
    }
});

// Reset row opacity after a drag ends (mirrors the workspace tool).
document.addEventListener('dragend', (e) => {
    if (e.target.classList && e.target.classList.contains('repo-row')) e.target.style.opacity = '';
});

document.getElementById('btn-refresh').addEventListener('click', refreshData);

document.getElementById('btn-fetch').addEventListener('click', async () => {
    if (!currentRepo) { showError('リポジトリが選択されていません'); return; }
    const btn = document.getElementById('btn-fetch');
    btn.disabled = true;
    btn.classList.add('active');
    try {
        const res = await fetch('/api/git/fetch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path: currentRepo })
        }).then(r => r.json());
        if (res.error) {
            showError(`Fetch failed: ${res.error}`);
        } else {
            await refreshData();
        }
    } catch (e) {
        showError(`Fetch failed: ${e.message}`);
    } finally {
        btn.disabled = false;
        btn.classList.remove('active');
    }
});

document.querySelectorAll('.tab').forEach(t => {
    t.addEventListener('click', () => {
        document.querySelectorAll('.tab').forEach(tt => tt.classList.remove('active'));
        t.classList.add('active');
        activeTab = t.dataset.tab;
        try { localStorage.setItem(TAB_STORAGE_KEY, activeTab); } catch (e) {}
        selectedItemIndex = 0;
        document.getElementById('right-pane').innerHTML = '';
        renderTabContent();
    });
});

