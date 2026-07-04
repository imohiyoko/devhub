// --- Worktree bulk removal -------------------------------------------------
// renderWtBulkBar fills the worktrees-list action bar from the current checkbox
// selection. Hidden when nothing is selected so the list looks unchanged until
// the user starts picking worktrees.
function renderWtBulkBar(bar) {
    if (!bar) return;
    bar.innerHTML = '';
    const n = selectedWorktreePaths.size;
    if (n === 0) { bar.style.display = 'none'; return; }
    bar.style.cssText = 'display:flex;align-items:center;gap:8px;padding:6px 10px;border-bottom:1px solid var(--border);background:var(--surface);';

    const label = document.createElement('span');
    label.style.cssText = 'font-size:12px;color:var(--text);';
    label.textContent = `${n} 件選択中`;
    bar.appendChild(label);

    const removeBtn = document.createElement('button');
    removeBtn.type = 'button';
    removeBtn.textContent = '🗑 選択を削除';
    removeBtn.style.cssText = 'background:transparent;border:1px solid var(--red);color:var(--red);padding:4px 10px;border-radius:4px;cursor:pointer;font-size:12px;';
    removeBtn.onclick = () => bulkRemoveSelectedWorktrees(removeBtn);
    bar.appendChild(removeBtn);

    const clearBtn = document.createElement('button');
    clearBtn.type = 'button';
    clearBtn.textContent = 'クリア';
    clearBtn.style.cssText = 'background:transparent;border:none;color:var(--muted);padding:4px 6px;cursor:pointer;font-size:12px;';
    clearBtn.onclick = () => { selectedWorktreePaths.clear(); renderTabContent(); };
    bar.appendChild(clearBtn);
}

// Remove every checked worktree in one pass, reusing the single-remove endpoint.
// Mirrors the merged-cleanup flow: a plain remove first, then any failures
// (typically uncommitted changes / locks) are handed to the existing
// force-remove recovery toast so the user can retry with --force.
async function bulkRemoveSelectedWorktrees(btn) {
    const repoAtShow = currentRepo;
    const targets = (gitData.worktrees || [])
        .filter(wt => selectedWorktreePaths.has(normalizePath(wt.path)));
    if (targets.length === 0) return;
    if (!confirm(`選択した ${targets.length} 件の worktree を削除しますか？`)) return;

    const originalText = btn?.textContent;
    if (btn) { btn.disabled = true; btn.textContent = '削除中...'; }
    let rendered = false;
    try {
        let ok = 0; const failed = [];
        for (const wt of targets) {
            try {
                const res = await fetch('/api/git/worktree/remove', {
                    method: 'POST', headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: repoAtShow, worktree_path: wt.path, force: false })
                }).then(r => r.json());
                if (res.error) { failed.push({ wt, error: res.error }); continue; }
                ok++;
            } catch (e) { failed.push({ wt, error: e.message }); }
        }
        if (ok > 0) showRepoMsg(`${ok} 件の worktree を削除しました`, 'success');
        selectedWorktreePaths.clear();
        await refreshData();
        selectedItemIndex = 0;
        renderTabContent();
        rendered = true;
        if (failed.length) showForceRemoveToast(failed, repoAtShow);
    } finally {
        if (!rendered && btn) {
            btn.disabled = false;
            btn.textContent = originalText;
        }
    }
}

// --- Worktree cleanup suggestions -----------------------------------------
// Inspect the freshly-fetched worktree list and surface up to two suggestion
// toasts, one per reason: branches already merged into the base branch, and
// worktrees whose directory has been deleted (stale admin entries).
function checkWorktreeCleanup() {
    if (!currentRepo) return;
    const wts = gitData.worktrees || [];
    const isMain = wt => normalizePath(wt.path) === normalizePath(currentRepo);

    // Missing-dir worktrees take priority: a merged worktree whose dir is also
    // gone belongs in the "missing" bucket (cleaned via prune), not both.
    const missing = wts.filter(wt => !isMain(wt) && !wt.bare && wt.exists === false);
    const merged = wts.filter(wt => !isMain(wt) && !wt.bare && wt.branch && wt.merged && wt.exists !== false);

    // Merged LOCAL branches that are not checked out in any worktree → safe to
    // delete on their own. A merged branch that still has a worktree is handled
    // by the 'merged' toast (its worktree must be removed before the branch can
    // be deleted); once that worktree is gone it surfaces here on the next poll.
    const wtBranches = new Set(wts.map(w => w.branch).filter(Boolean));
    const branchItems = (gitData.mergedBranches || [])
        .filter(b => !wtBranches.has(b))
        .map(b => ({ branch: b, path: `branch:${b}` }));

    // Closed (unmerged) PR branches that exist locally but the PR was closed.
    const closedItems = (gitData.closedBranches || [])
        .filter(b => !wtBranches.has(b))
        .map(b => ({ branch: b, path: `closed:${b}` }));

    maybeShowCleanupToast('merged', merged);
    maybeShowCleanupToast('missing', missing);
    maybeShowCleanupToast('branches', branchItems);
    maybeShowCleanupToast('closed', closedItems);
}

function maybeShowCleanupToast(category, items) {
    // Skip while a toast for this category is still open, and only consider
    // items the user hasn't already dismissed/handled this session.
    if (activeCleanupToasts[category]) return;
    const dismissed = dismissedCleanup[category];
    const fresh = items.filter(it => !dismissed.has(`${currentRepo}|${it.path}`));
    if (fresh.length === 0) return;
    showCleanupToast(category, fresh);
}

// Remove each merged worktree, optionally deleting its (now-merged) local
// branch afterwards. `git worktree remove` keeps the branch; the optional
// `git branch -d` deletes it once the worktree no longer holds it checked out.
async function _removeMergedWorktrees(items, repoAtShow, deleteBranch) {
    let ok = 0; const failed = [];
    for (const wt of items) {
        try {
            const res = await fetch('/api/git/worktree/remove', {
                method: 'POST', headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: repoAtShow, worktree_path: wt.path, force: false })
            }).then(r => r.json());
            if (res.error) { failed.push({ wt, error: res.error }); continue; }
            ok++;
            if (deleteBranch && wt.branch) {
                // force (-D): the backend already confirmed this branch is merged
                // into the base (wt.merged). Plain -d would fail for PR-merged
                // branches whose remote tracking branch was deleted ("not fully
                // merged into HEAD"), which is the common case, so the merged gate
                // is what makes -D safe here.
                const del = await fetch('/api/git/branch/delete', {
                    method: 'POST', headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: repoAtShow, branch: wt.branch, force: true })
                }).then(r => r.json());
                if (del.error) failed.push({ wt, error: `ブランチ削除失敗: ${del.error}` });
            }
        } catch (e) { failed.push({ wt, error: e.message }); }
    }
    if (ok > 0) showRepoMsg(`${ok} 件の worktree を削除しました`, 'success');
    if (failed.length) showForceRemoveToast(failed, repoAtShow);
}

// Show a recovery toast when batch worktree removal partially fails.
// Each failed item gets an "Open Worktree" button; a single "Force Remove"
// button retries all failed items with --force.
function showForceRemoveToast(failedItems, repoAtShow) {
    const container = document.getElementById('toast-container');
    const toast = document.createElement('div');
    toast.className = 'toast cleanup info';

    const closeToast = () => { if (toast.parentNode) toast.remove(); };

    const head = document.createElement('div'); head.className = 'toast-head';
    const titleEl = document.createElement('span'); titleEl.className = 'toast-title';
    titleEl.textContent = `${failedItems.length} 件の worktree を削除できませんでした（未コミットの変更があります）`;
    const closeBtn = document.createElement('span'); closeBtn.className = 'close'; closeBtn.textContent = '×';
    closeBtn.onclick = closeToast;
    head.appendChild(titleEl); head.appendChild(closeBtn);
    toast.appendChild(head);

    const list = document.createElement('ul'); list.className = 'toast-list';
    failedItems.forEach(({ wt }) => {
        const li = document.createElement('li');
        li.style.display = 'flex'; li.style.alignItems = 'center'; li.style.gap = '6px';
        const branch = wt.branch || '(detached)';
        const folder = (wt.path.split(/[\\/]/).filter(Boolean).pop()) || wt.path;
        const label = document.createElement('span');
        label.innerHTML = `<span class="wt-branch">${escapeHtml(branch)}</span> <span>[${escapeHtml(folder)}]</span>`;
        label.title = wt.path;
        li.appendChild(label);

        const btnWrap = document.createElement('span');
        btnWrap.style.cssText = 'margin-left:auto;flex-shrink:0;display:flex;gap:4px';

        const wsBtn = document.createElement('button');
        wsBtn.type = 'button'; wsBtn.className = 'toast-undo'; wsBtn.textContent = 'ワークスペースで開く';
        wsBtn.style.cssText = 'padding:2px 8px;font-size:11px';
        wsBtn.onclick = () => { fetch(`/api/open?path=${encodeURIComponent(wt.path)}`); };
        btnWrap.appendChild(wsBtn);

        const openBtn = document.createElement('button');
        openBtn.type = 'button'; openBtn.className = 'toast-undo'; openBtn.textContent = 'Worktreeを開く';
        openBtn.style.cssText = 'padding:2px 8px;font-size:11px';
        openBtn.onclick = () => {
            document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
            const wtTab = document.querySelector('.tab[data-tab="worktrees"]');
            if (wtTab) wtTab.classList.add('active');
            activeTab = 'worktrees';
            const idx = (gitData.worktrees || []).findIndex(w => normalizePath(w.path) === normalizePath(wt.path));
            if (idx >= 0) selectedItemIndex = idx;
            renderTabContent();
            closeToast();
        };
        btnWrap.appendChild(openBtn);
        li.appendChild(btnWrap);
        list.appendChild(li);
    });
    toast.appendChild(list);

    const actions = document.createElement('div'); actions.className = 'toast-actions';
    const forceBtn = document.createElement('button');
    forceBtn.type = 'button'; forceBtn.className = 'toast-confirm';
    forceBtn.textContent = '強制削除（未保存の変更は失われます）';
    forceBtn.onclick = async () => {
        forceBtn.disabled = true; forceBtn.textContent = '処理中...';
        let ok = 0; const errors = [];
        for (const { wt } of failedItems) {
            try {
                const res = await fetch('/api/git/worktree/remove', {
                    method: 'POST', headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: repoAtShow, worktree_path: wt.path, force: true })
                }).then(r => r.json());
                if (res.error) { errors.push(`${wt.branch || '(detached)'}: ${res.error}`); } else { ok++; }
            } catch (e) { errors.push(`${wt.branch}: ${e.message}`); }
        }
        if (ok > 0) showRepoMsg(`${ok} 件の worktree を強制削除しました`, 'success');
        if (errors.length) showError(`強制削除に失敗: ${errors.join(' / ')}`);
        await refreshData();
        selectedItemIndex = 0;
        renderTabContent();
        closeToast();
    };
    actions.appendChild(forceBtn);
    toast.appendChild(actions);

    container.appendChild(toast);
}

// _deleteLocalBranches force-deletes a list of local branches and reports results.
// Used by both merged-branch cleanup and closed-PR-branch cleanup.
async function _deleteLocalBranches(items, repoAtShow, successMsg) {
    let ok = 0; const failed = [];
    for (const it of items) {
        try {
            const res = await fetch('/api/git/branch/delete', {
                method: 'POST', headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: repoAtShow, branch: it.branch, force: true })
            }).then(r => r.json());
            if (res.error) { failed.push(`${it.branch}: ${res.error}`); } else { ok++; }
        } catch (e) { failed.push(`${it.branch}: ${e.message}`); }
    }
    if (ok > 0) showRepoMsg(successMsg.replace('{n}', ok), 'success');
    if (failed.length) showError(`一部失敗: ${failed.join(' / ')}`);
}

async function _pruneMissingWorktrees(items, repoAtShow) {
    const res = await fetch('/api/git/worktree/prune', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path: repoAtShow })
    }).then(r => r.json());
    if (res.error) { showError(`クリーンアップに失敗: ${res.error}`); return; }
    // prune は repo 全体の prunable をまとめて消すため、提案一覧 (items) の件数とは
    // 一致しないことがある。実際に消えた件数は `prune -v` の "Removing ..." 行から数える
    // （取れない場合は件数を出さず汎用メッセージにする）。
    const removed = (res.output || '').split('\n').filter(l => /^Removing\b/.test(l)).length;
    showRepoMsg(removed ? `${removed} 件の不要な worktree を整理しました` : '不要な worktree を整理しました', 'success');
}

function showCleanupToast(category, items) {
    const repoAtShow = currentRepo;
    const baseShort = gitData.baseBranch ? `${gitData.baseBranch} にマージ済み` : 'マージ済み';
    const container = document.getElementById('toast-container');
    const toast = document.createElement('div');
    toast.className = 'toast cleanup info';
    activeCleanupToasts[category] = toast;

    const markHandled = () => items.forEach(it => dismissedCleanup[category].add(`${repoAtShow}|${it.path}`));
    const closeToast = () => {
        if (toast.parentNode) toast.remove();
        if (activeCleanupToasts[category] === toast) activeCleanupToasts[category] = null;
    };

    // Per-category copy and actions. Each action returns a promise; the runner
    // disables all buttons, marks the items handled (so a partial failure does
    // not re-popup every poll), refreshes, and closes the toast.
    const titles = {
        merged: `マージ済みブランチの worktree が ${items.length} 件あります（${baseShort}）`,
        missing: `ディレクトリが見つからない worktree が ${items.length} 件あります`,
        branches: `マージ済みのローカルブランチが ${items.length} 件あります（worktree なし・${baseShort}）`,
        closed: `クローズ済み PR のローカルブランチが ${items.length} 件あります（未マージの可能性あり・削除すると作業が失われます）`,
    };
    const actionDefs = {
        merged: [
            { label: 'worktree とブランチを削除', cls: 'toast-confirm', run: () => _removeMergedWorktrees(items, repoAtShow, true) },
            { label: 'worktree のみ', cls: 'toast-undo', run: () => _removeMergedWorktrees(items, repoAtShow, false) },
        ],
        missing: [
            { label: 'クリーンアップ', cls: 'toast-confirm', run: () => _pruneMissingWorktrees(items, repoAtShow) },
        ],
        branches: [
            { label: 'すべて削除', cls: 'toast-confirm', run: () => _deleteLocalBranches(items, repoAtShow, '{n} 件のローカルブランチを削除しました') },
        ],
        closed: [
            { label: '強制削除（未マージ作業が消える恐れあり）', cls: 'toast-confirm', run: () => _deleteLocalBranches(items, repoAtShow, '{n} 件のローカルブランチを削除しました') },
        ],
    };

    const head = document.createElement('div'); head.className = 'toast-head';
    const titleEl = document.createElement('span'); titleEl.className = 'toast-title';
    titleEl.textContent = titles[category];
    const closeBtn = document.createElement('span'); closeBtn.className = 'close'; closeBtn.textContent = '×';
    // Dismiss: remember these so they are not re-suggested again this session.
    closeBtn.onclick = () => { markHandled(); closeToast(); };
    head.appendChild(titleEl); head.appendChild(closeBtn);
    toast.appendChild(head);

    const list = document.createElement('ul'); list.className = 'toast-list';
    items.forEach(it => {
        const li = document.createElement('li');
        const branch = it.branch || (it.bare ? '(bare)' : '(detached)');
        if (category === 'branches' || category === 'closed') {
            li.innerHTML = `<span class="wt-branch">${escapeHtml(branch)}</span>`;
        } else {
            const folder = (it.path.split(/[\\/]/).filter(Boolean).pop()) || it.path;
            li.innerHTML = `<span class="wt-branch">${escapeHtml(branch)}</span> <span>[${escapeHtml(folder)}]</span>`;
            li.title = it.path;
        }
        list.appendChild(li);
    });
    toast.appendChild(list);

    const actions = document.createElement('div'); actions.className = 'toast-actions';
    const buttons = [];
    const runAction = async (clicked, fn) => {
        buttons.forEach(b => { b.disabled = true; });
        const orig = clicked.textContent;
        clicked.textContent = '処理中...';
        markHandled();
        try {
            await fn();
            await refreshData();
            selectedItemIndex = 0;
            renderTabContent();
        } catch (e) {
            showError(`クリーンアップに失敗しました: ${e.message}`);
        } finally {
            closeToast();
        }
    };
    actionDefs[category].forEach(def => {
        const btn = document.createElement('button');
        btn.type = 'button'; btn.className = def.cls; btn.textContent = def.label;
        btn.onclick = () => runAction(btn, def.run);
        buttons.push(btn);
        actions.appendChild(btn);
    });
    toast.appendChild(actions);

    container.appendChild(toast);
}

