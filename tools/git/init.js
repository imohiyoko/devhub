// Init
(async () => {
    // Sync the visually active tab with the persisted activeTab (the HTML
    // markup hardcodes 'status' as active).
    document.querySelectorAll('.tab').forEach(t => t.classList.toggle('active', t.dataset.tab === activeTab));
    await loadSystemInfo();
    await loadAppConfig();
    await loadSettings();
    await fetchRepos();
    await refreshData();
    startPolling();
})();

