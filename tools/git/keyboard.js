// Keyboard
document.addEventListener('keydown', (e) => {
    // Ignore if focus is in input/textarea
    if(document.activeElement.tagName === 'INPUT' || document.activeElement.tagName === 'TEXTAREA' || document.activeElement.tagName === 'SELECT') {
        return;
    }

    const k = gitConfig.keybindings;

    if (['1','2','3','4','5','6'].includes(e.key)) {
        document.querySelector(`.tab[data-tab="${VALID_TABS[parseInt(e.key)-1]}"]`).click();
        return;
    }

    if (e.key === 'ArrowDown') {
        if (selectedItemIndex < currentListItems.length - 1) { selectedItemIndex++; renderTabContent(); }
        e.preventDefault(); return;
    }
    if (e.key === 'ArrowUp') {
        if (selectedItemIndex > 0) { selectedItemIndex--; renderTabContent(); }
        e.preventDefault(); return;
    }

    if (e.key === k.refresh) { refreshData(); return; }
    if (e.key === k.push) { doPost('/api/git/push', {}); return; }
    if (e.key === k.pull) { doPost('/api/git/pull', {}); return; }

    const selObj = currentListItems[selectedItemIndex];

    if (activeTab === 'status') {
        if (e.key === k.stage && selObj) {
            const endpoint = selObj.isStaged ? '/api/git/unstage' : '/api/git/stage';
            doPost(endpoint, { files: [selObj.data.file] });
        } else if (e.key === k.commit) {
            const ta = document.getElementById('commit-msg');
            if(ta) { ta.focus(); e.preventDefault(); }
        }
    } else if (activeTab === 'branches') {
        if (e.key === 'Enter' && selObj) {
            let bn = selObj.data.trim();
            const localBranches = getLocalBranches();
            if (bn.includes('/') && !localBranches.includes(bn)) {
                const parts = bn.split('/');
                if (parts.length > 1) {
                    bn = parts.slice(1).join('/');
                }
            }
            doPost('/api/git/checkout', { branch: bn });
        } else if (e.key === k.new_branch) {
            const bn = prompt('New branch name:');
            if(bn) doPost('/api/git/branch/create', { branch: bn });
        }
    } else if (activeTab === 'stash') {
        if (e.key === ' ' && selObj) { // space to pop
             // assuming format 'stash@{index}: ...'
             const match = selObj.data.match(/stash@\{(\d+)\}/);
             if(match) doPost('/api/git/stash', { action: 'pop', index: parseInt(match[1], 10) });
        } else if (e.key === 'd' && selObj) {
             const match = selObj.data.match(/stash@\{(\d+)\}/);
             if(match) doPost('/api/git/stash', { action: 'drop', index: parseInt(match[1], 10) });
        }
    }
});


