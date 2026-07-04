function showAddWorktreeForm(preselectedBranch = '') {
    document.querySelectorAll('.tab').forEach(tt => tt.classList.remove('active'));
    const wtTab = document.querySelector('.tab[data-tab="worktrees"]');
    if (wtTab) wtTab.classList.add('active');
    activeTab = 'worktrees';
    renderTabContent();
    renderAddWorktreeForm(preselectedBranch);
}

function renderAddWorktreeForm(preselectedBranch = '') {
    const right = document.getElementById('right-pane');
    right.innerHTML = '';
    
    const container = document.createElement('div');
    container.style.padding = '20px';
    container.style.display = 'flex';
    container.style.flexDirection = 'column';
    container.style.gap = '16px';
    container.style.maxWidth = '500px';
    
    const title = document.createElement('h2');
    title.style.fontSize = '18px';
    title.style.fontWeight = '600';
    title.style.color = 'var(--text)';
    title.textContent = 'Add Git Worktree';
    container.appendChild(title);

    // --- From Pull Request -------------------------------------------------
    // Paste a GitHub PR URL and fetch its head into a brand-new worktree in one
    // step. The branch is named after the PR's real head branch (resolved via
    // gh on the server) when possible, otherwise pr-<number>.
    const prCard = document.createElement('div');
    prCard.style.background = 'var(--surface)';
    prCard.style.border = '1px solid var(--border)';
    prCard.style.borderRadius = '6px';
    prCard.style.padding = '14px';
    prCard.style.display = 'flex';
    prCard.style.flexDirection = 'column';
    prCard.style.gap = '8px';
    prCard.innerHTML = `
        <label style="font-weight: 600; font-size: 13px; color: var(--text);">From Pull Request</label>
        <input type="text" id="wt-add-pr-url" placeholder="https://github.com/owner/repo/pull/123"
               style="background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 8px; border-radius: 4px; font-family: monospace;">
        <span style="font-size: 11px; color: var(--muted);">PR の head ブランチを fetch して worktree を作成します（ブランチ名は gh で解決、不可なら pr-&lt;番号&gt;）。</span>`;
    const prBtn = makeButton('Create from PR', 'primary');
    prBtn.style.alignSelf = 'flex-start';
    prBtn.style.padding = '8px 16px';
    prBtn.onclick = async () => {
        const prUrl = prCard.querySelector('#wt-add-pr-url').value.trim();
        if (!prUrl) { showError('PR URL is required'); return; }
        prBtn.disabled = true;
        prBtn.textContent = 'Fetching PR…';
        try {
            const res = await fetch('/api/git/worktree/from-pr', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: currentRepo, pr_url: prUrl })
            }).then(r => r.json());

            if (res.error) {
                showError(`Error: ${res.error}`);
            } else {
                const reusedNote = res.reused_branch
                    ? (res.at_pr_head ? '（既存ブランチを再利用・最新の PR head へ ff 更新）' : '（既存ブランチを再利用・diverged のため更新せず据え置き）')
                    : '';
                showRepoMsg(`Worktree created from PR #${res.pr_number} (${res.branch})${reusedNote}`, 'success');
                await refreshData();
                const index = gitData.worktrees.findIndex(w => normalizePath(w.path) === normalizePath(res.worktree_path));
                selectedItemIndex = index !== -1 ? index + 1 : 0;
                renderTabContent();
            }
        } catch (e) {
            showError(`Failed to create worktree from PR: ${e.message}`);
        } finally {
            prBtn.disabled = false;
            prBtn.textContent = 'Create from PR';
        }
    };
    prCard.appendChild(prBtn);
    container.appendChild(prCard);

    const divider = document.createElement('div');
    divider.style.borderTop = '1px solid var(--border)';
    divider.style.margin = '4px 0';
    const dividerLabel = document.createElement('span');
    dividerLabel.textContent = 'または、ブランチを指定して作成';
    dividerLabel.style.fontSize = '11px';
    dividerLabel.style.color = 'var(--muted)';
    container.appendChild(divider);
    container.appendChild(dividerLabel);

    const pathGroup = document.createElement('div');
    pathGroup.style.display = 'flex';
    pathGroup.style.flexDirection = 'column';
    pathGroup.style.gap = '6px';
    
    let defaultPath = '';
    if (currentRepo && preselectedBranch) {
        const sanitizedBranch = preselectedBranch.replace(/[^a-zA-Z0-9_-]/g, '-');
        defaultPath = `${currentRepo}-wt-${sanitizedBranch}`;
    } else if (currentRepo) {
        defaultPath = `${currentRepo}-wt`;
    }
    
    pathGroup.innerHTML = `<label style="font-weight: 500; font-size: 13px; color: var(--text);">Worktree Path:</label>
                           <input type="text" id="wt-add-path" value="${escapeHtml(defaultPath)}" placeholder="e.g. C:/projects/myrepo-worktree" style="background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 8px; border-radius: 4px; font-family: monospace;">
                           <span style="font-size: 11px; color: var(--muted);">Specify an empty directory path where the worktree will be created.</span>`;
    container.appendChild(pathGroup);
    
    const branchGroup = document.createElement('div');
    branchGroup.style.display = 'flex';
    branchGroup.style.flexDirection = 'column';
    branchGroup.style.gap = '6px';
    branchGroup.innerHTML = `<label style="font-weight: 500; font-size: 13px; color: var(--text);">Checkout Branch:</label>
                             <select id="wt-add-branch-select" style="background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 8px; border-radius: 4px; width: 100%;"></select>`;
    container.appendChild(branchGroup);
    
    const branchSelect = branchGroup.querySelector('#wt-add-branch-select');
    const localBranches = getLocalBranches();
    
    localBranches.forEach(b => {
        const opt = document.createElement('option');
        opt.value = b;
        opt.textContent = b;
        if (b === preselectedBranch) opt.selected = true;
        branchSelect.appendChild(opt);
    });
    
    const cbGroup = document.createElement('div');
    cbGroup.style.display = 'flex';
    cbGroup.style.alignItems = 'center';
    cbGroup.style.gap = '8px';
    cbGroup.style.marginTop = '4px';
    cbGroup.innerHTML = `<input type="checkbox" id="wt-add-new-branch-cb">
                         <label for="wt-add-new-branch-cb" style="font-weight: 500; font-size: 13px; color: var(--text); user-select: none;">Create a new branch instead</label>`;
    container.appendChild(cbGroup);
    
    const newBranchSection = document.createElement('div');
    newBranchSection.id = 'wt-add-new-branch-section';
    newBranchSection.style.display = 'none';
    newBranchSection.style.flexDirection = 'column';
    newBranchSection.style.gap = '12px';
    newBranchSection.style.borderLeft = '2px solid var(--accent)';
    newBranchSection.style.paddingLeft = '12px';
    newBranchSection.style.marginLeft = '4px';
    
    newBranchSection.innerHTML = `
        <div style="display: flex; flex-direction: column; gap: 6px;">
            <label style="font-weight: 500; font-size: 13px; color: var(--text);">New Branch Name:</label>
            <input type="text" id="wt-add-new-branch-name" placeholder="e.g. feature/my-cool-feature" style="background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 8px; border-radius: 4px; font-family: monospace;">
        </div>
        <div style="display: flex; flex-direction: column; gap: 6px;">
            <label style="font-weight: 500; font-size: 13px; color: var(--text);">Base Commit/Branch:</label>
            <select id="wt-add-base-select" style="background: var(--bg); border: 1px solid var(--border); color: var(--text); padding: 8px; border-radius: 4px; width: 100%;"></select>
        </div>
    `;
    container.appendChild(newBranchSection);
    
    const baseSelect = newBranchSection.querySelector('#wt-add-base-select');
    const allBranches = getAllBranchesList();
    allBranches.forEach(b => {
        const opt = document.createElement('option');
        opt.value = b;
        opt.textContent = b;
        baseSelect.appendChild(opt);
    });
    
    const cb = cbGroup.querySelector('#wt-add-new-branch-cb');
    const nameInput = newBranchSection.querySelector('#wt-add-new-branch-name');
    nameInput.addEventListener('input', () => {
        if (cb.checked) {
            const name = nameInput.value.trim().replace(/[^a-zA-Z0-9_-]/g, '-');
            document.getElementById('wt-add-path').value = `${currentRepo}-wt-${name || 'newbranch'}`;
        }
    });

    cb.addEventListener('change', (e) => {
        const checked = e.target.checked;
        newBranchSection.style.display = checked ? 'flex' : 'none';
        branchSelect.disabled = checked;
        
        if (!checked) {
            const branch = branchSelect.value;
            const sanitizedBranch = branch.replace(/[^a-zA-Z0-9_-]/g, '-');
            document.getElementById('wt-add-path').value = `${currentRepo}-wt-${sanitizedBranch}`;
        } else {
            const name = nameInput.value.trim().replace(/[^a-zA-Z0-9_-]/g, '-');
            document.getElementById('wt-add-path').value = `${currentRepo}-wt-${name || 'newbranch'}`;
        }
    });
    
    branchSelect.addEventListener('change', (e) => {
        if (!cb.checked) {
            const branch = e.target.value;
            const sanitizedBranch = branch.replace(/[^a-zA-Z0-9_-]/g, '-');
            document.getElementById('wt-add-path').value = `${currentRepo}-wt-${sanitizedBranch}`;
        }
    });

    const btnRow = document.createElement('div');
    btnRow.style.display = 'flex';
    btnRow.style.gap = '12px';
    btnRow.style.marginTop = '8px';
    
    const btnSubmit = makeButton('Create Worktree', 'primary');
    btnSubmit.style.padding = '10px 20px';
    btnSubmit.onclick = async () => {
        const wtPath = document.getElementById('wt-add-path').value.trim();
        if (!wtPath) { alert('Path is required'); return; }
        
        const isNewBranch = cb.checked;
        let branch = '';
        let baseCommit = '';
        
        if (isNewBranch) {
            branch = document.getElementById('wt-add-new-branch-name').value.trim();
            if (!branch) { alert('New branch name is required'); return; }
            baseCommit = document.getElementById('wt-add-base-select').value;
        } else {
            branch = branchSelect.value;
            if (!branch) { alert('Select a branch to checkout'); return; }
        }
        
        btnSubmit.disabled = true;
        btnSubmit.textContent = 'Creating...';
        
        try {
            const res = await fetch('/api/git/worktree/add', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    path: currentRepo,
                    worktree_path: wtPath,
                    branch: branch,
                    new_branch: isNewBranch,
                    base_commit: baseCommit || undefined
                })
            }).then(r => r.json());
            
            if (res.error) {
                showError(`Error: ${res.error}`);
            } else {
                showRepoMsg('Worktree created successfully', 'success');
                await refreshData();
                const index = gitData.worktrees.findIndex(w => normalizePath(w.path) === normalizePath(wtPath));
                selectedItemIndex = index !== -1 ? index + 1 : 0;
                renderTabContent();
            }
        } catch (e) {
            showError(`Failed to create worktree: ${e.message}`);
        } finally {
            btnSubmit.disabled = false;
            btnSubmit.textContent = 'Create Worktree';
        }
    };
    
    btnRow.appendChild(btnSubmit);
    container.appendChild(btnRow);
    right.appendChild(container);
}

function renderWorktreeDetails(wt, isMain) {
    const right = document.getElementById('right-pane');
    right.innerHTML = '';
    
    const container = document.createElement('div');
    container.style.padding = '20px';
    container.style.display = 'flex';
    container.style.flexDirection = 'column';
    container.style.gap = '16px';
    
    const title = document.createElement('h2');
    title.style.fontSize = '18px';
    title.style.fontWeight = '600';
    title.style.color = 'var(--text)';
    title.textContent = 'Worktree Details';
    container.appendChild(title);
    
    const card = document.createElement('div');
    card.style.background = 'var(--surface)';
    card.style.border = '1px solid var(--border)';
    card.style.borderRadius = '6px';
    card.style.padding = '16px';
    card.style.display = 'flex';
    card.style.flexDirection = 'column';
    card.style.gap = '12px';
    
    card.appendChild(makeInfoRow('Path',
        `<span style="font-family: monospace; font-size: 13px; color: var(--text); word-break: break-all;">${escapeHtml(wt.path)}</span>`));

    card.appendChild(makeInfoRow('Branch',
        `<span style="font-family: monospace; font-size: 14px; color: var(--green);">${escapeHtml(wt.branch || (wt.bare ? '(bare)' : '(detached)'))}</span>`));

    if (wt.head) {
        card.appendChild(makeInfoRow('Commit SHA',
            `<span style="font-family: monospace; font-size: 13px; color: var(--blue);">${escapeHtml(wt.head)}</span>`));
    }

    const envName = (gitData.launchedByPath || {})[normalizePath(wt.path)];
    if (envName) {
        card.appendChild(makeInfoRow('env-launcher',
            `🚀 <span style="color: var(--accent);">${escapeHtml(envName)}</span> で起動 — ` +
            `<a href="/env-launcher" style="color: var(--accent);">env launcher で管理</a>`));
    }

    container.appendChild(card);
    
    const btnRow = document.createElement('div');
    btnRow.style.display = 'flex';
    btnRow.style.gap = '10px';
    btnRow.style.flexWrap = 'wrap';
    
    const btnOpen = makeButton('Open in Editor', 'primary');
    btnOpen.onclick = () => {
        fetch(`/api/open?path=${encodeURIComponent(wt.path)}`);
    };
    btnRow.appendChild(btnOpen);

    const btnPull = makeButton('Pull', 'secondary');
    btnPull.onclick = async () => {
        btnPull.disabled = true;
        btnPull.textContent = 'Pulling...';
        try {
            const res = await fetch('/api/git/worktree/pull', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: currentRepo, worktree_path: wt.path })
            }).then(r => r.json());
            if (res.error) {
                showError(`Pull failed: ${res.error}`);
            } else {
                showRepoMsg((res.output || '').trim() || 'Pull complete', 'success');
                await refreshData();
            }
        } catch (e) {
            showError(`Pull failed: ${e.message}`);
        } finally {
            btnPull.disabled = false;
            btnPull.textContent = 'Pull';
        }
    };
    btnRow.appendChild(btnPull);

    const btnPush = makeButton('Push', 'secondary');
    btnPush.onclick = async () => {
        btnPush.disabled = true;
        btnPush.textContent = 'Pushing...';
        try {
            const res = await fetch('/api/git/worktree/push', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: currentRepo, worktree_path: wt.path })
            }).then(r => r.json());
            if (res.error) {
                showError(`Push failed: ${res.error}`);
            } else {
                showRepoMsg((res.output || '').trim() || 'Push complete', 'success');
                await refreshData();
            }
        } catch (e) {
            showError(`Push failed: ${e.message}`);
        } finally {
            btnPush.disabled = false;
            btnPush.textContent = 'Push';
        }
    };
    btnRow.appendChild(btnPush);

    if (!isMain) {
        const btnRemove = makeButton('Remove Worktree', 'danger');
        btnRemove.onclick = async () => {
            if (confirm(`Are you sure you want to remove the worktree at "${wt.path}"?`)) {
                btnRemove.disabled = true;
                btnRemove.textContent = 'Removing...';
                try {
                    const res = await fetch('/api/git/worktree/remove', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ path: currentRepo, worktree_path: wt.path, force: false })
                    }).then(r => r.json());
                    if (res.error) {
                        showError(`Error: ${res.error}`);
                    } else {
                        showRepoMsg('Worktree removed successfully', 'success');
                        await refreshData();
                        selectedItemIndex = 0;
                        renderTabContent();
                    }
                } catch (e) {
                    showError(`Failed to remove worktree: ${e.message}`);
                } finally {
                    btnRemove.disabled = false;
                    btnRemove.textContent = 'Remove Worktree';
                }
            }
        };
        btnRow.appendChild(btnRemove);
        
        const btnRemoveForce = makeButton('Force Remove', 'danger');
        btnRemoveForce.style.opacity = '0.7';
        btnRemoveForce.onclick = async () => {
            if (confirm(`Force remove worktree at "${wt.path}"? (Uncommitted changes will be lost)`)) {
                btnRemoveForce.disabled = true;
                btnRemoveForce.textContent = 'Removing...';
                try {
                    const res = await fetch('/api/git/worktree/remove', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ path: currentRepo, worktree_path: wt.path, force: true })
                    }).then(r => r.json());
                    if (res.error) {
                        showError(`Error: ${res.error}`);
                    } else {
                        showRepoMsg('Worktree force-removed successfully', 'success');
                        await refreshData();
                        selectedItemIndex = 0;
                        renderTabContent();
                    }
                } catch (e) {
                    showError(`Failed to remove worktree: ${e.message}`);
                } finally {
                    btnRemoveForce.disabled = false;
                    btnRemoveForce.textContent = 'Force Remove';
                }
            }
        };
        btnRow.appendChild(btnRemoveForce);
    }
    
    container.appendChild(btnRow);
    right.appendChild(container);
}

