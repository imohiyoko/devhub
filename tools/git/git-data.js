// Git Operations
async function refreshData(withSuggestion = true) {
    if (!currentRepo) return;

    try {
        // Only ask the server to compute the poll-interval suggestion (an extra
        // `git log`) when withSuggestion is set. The high-frequency local timer
        // passes false; everything else (init, remote poll, manual ops) lets it
        // default to true so the suggestion refreshes on the slower cadence.
        const suggestQuery = withSuggestion ? '&suggest=1' : '';
        // The worktree list rarely changes and `git worktree list` is relatively
        // expensive, so skip it on the high-frequency local poll
        // (withSuggestion=false) and keep the previously fetched
        // gitData.worktrees. It refreshes on the slower cadence (init / remote
        // poll / manual ops) alongside the suggestion.
        const reqs = [
            fetch(`/api/git/status?path=${encodeURIComponent(currentRepo)}${suggestQuery}`).then(r=>r.json()),
            fetch(`/api/git/log?path=${encodeURIComponent(currentRepo)}&n=${gitConfig.log_limit}`).then(r=>r.json()),
            fetch(`/api/git/branches?path=${encodeURIComponent(currentRepo)}`).then(r=>r.json()),
            fetch(`/api/git/stash/list?path=${encodeURIComponent(currentRepo)}`).then(r=>r.json()),
        ];
        if (withSuggestion) {
            reqs.push(fetch(`/api/git/worktrees?path=${encodeURIComponent(currentRepo)}`).then(r=>r.json()));
            // env-launcher launch registry: used to badge worktrees devhub created.
            reqs.push(fetch('/api/envs/launches').then(r=>r.json()).catch(()=>({launches:[]})));
        }
        const [status, log, branches, stash, worktrees, launches] = await Promise.all(reqs);

        gitData.status = status.output || '';
        gitData.log = log.output || '';
        gitData.branches = branches.output || '';
        gitData.stash = stash.output || '';
        if (withSuggestion) {
            gitData.worktrees = worktrees.worktrees || [];
            gitData.baseBranch = worktrees.base_branch || null;
            gitData.mergedBranches = worktrees.merged_branches || [];
            gitData.closedBranches = worktrees.closed_branches || [];
            // env-launcher launch registry: used to badge worktrees devhub created.
            const map = {};
            ((launches && launches.launches) || []).forEach(l => {
                const envName = l.env_name || l.env_id;
                // env-level worktree plus any per-process bound worktrees.
                if (l.worktree_path) map[normalizePath(l.worktree_path)] = envName;
                (l.processes || []).forEach(p => {
                    if (p.worktree_path) map[normalizePath(p.worktree_path)] = envName;
                });
            });
            gitData.launchedByPath = map;
            // Worktrees were just refreshed (slow cadence): re-evaluate whether
            // any merged / missing-directory worktrees are worth suggesting for
            // cleanup. Skipped on the high-frequency local poll, which leaves
            // gitData.worktrees untouched.
            checkWorktreeCleanup();
        }
        // has_remote rides along on the suggest cadence; once we learn a repo has
        // no remote, adjustDynamicPolling stops arming the failing fetch timer.
        if ('has_remote' in status) gitData.hasRemote = status.has_remote;

        // Dynamic polling adjustment
        const localIsAuto = gitConfig.local_poll_interval === 'auto';
        const remoteIsAuto = gitConfig.remote_poll_interval === 'auto';
        if (localIsAuto || remoteIsAuto) {
            // Auto (or mixed) mode: only re-arm when this refresh actually carried a
            // fresh suggestion. The high-frequency local poll passes
            // withSuggestion=false and leaves the running timers untouched.
            // Each field is resolved independently so a manually-set side is never
            // overwritten by the server suggestion (matches startPolling()).
            if (withSuggestion) {
                const local = localIsAuto ? (status.suggested_local_interval || 600) : gitConfig.local_poll_interval;
                const remote = remoteIsAuto ? (status.suggested_remote_interval || 1800) : gitConfig.remote_poll_interval;
                adjustDynamicPolling(local, remote);
            }
        } else {
            adjustDynamicPolling(gitConfig.local_poll_interval, gitConfig.remote_poll_interval);
        }

        renderLog();
        lastFetchedDiff = null;
        if (selectedItemIndex >= currentListItems.length) selectedItemIndex = 0;
        renderTabContent();
    } catch (e) { console.error(e); }
}

function renderLog() {
    document.getElementById('log-content').textContent = gitData.log;
}

function parseStatus(statusStr) {
    const lines = statusStr.trim().split('\n').filter(l=>l);
    const staged = []; const unstaged = [];
    lines.forEach(line => {
        if(line.length < 3) return;
        const x = line[0]; const y = line[1]; const file = line.substring(3);
        if (x !== ' ' && x !== '?') staged.push({ status: x, file });
        if (y !== ' ') unstaged.push({ status: y, file });
    });
    return { staged, unstaged };
}

let selectedItemIndex = 0;
let currentListItems = []; // For keyboard navigation
let lastFetchedDiff = null;
// Normalized paths of worktrees checked in the list for bulk removal. Survives
// re-renders (so a slow poll mid-selection keeps the checks) and is pruned in
// renderTabContent once a worktree disappears.
let selectedWorktreePaths = new Set();

// Worktree cleanup suggestions. `dismissedCleanup` remembers which suggestions
// the user dismissed (or already acted on) so they are not re-shown on every
// poll; keys are `${currentRepo}|${path}` so dismissal is scoped per repo.
// `activeCleanupToasts` holds the currently-open toast per category to avoid
// stacking duplicates while one is already visible.
const dismissedCleanup = { merged: new Set(), missing: new Set(), branches: new Set(), closed: new Set() };
const activeCleanupToasts = { merged: null, missing: null, branches: null, closed: null };

function renderTabContent() {
    const container = document.getElementById('tab-content');
    container.innerHTML = '';
    currentListItems = [];

    if (activeTab === 'status') {
        const headBranch = parseBranchLines().find(b => b.isHead);
        const branch = headBranch ? headBranch.shortName : 'Unknown';
        const { staged, unstaged } = parseStatus(gitData.status);

        const branchHeader = document.createElement('div');
        branchHeader.style.cssText = 'padding:8px; font-weight:bold; border-bottom:1px solid var(--border); color:var(--green);';
        branchHeader.textContent = `Branch: ${branch}`;
        container.appendChild(branchHeader);

        const renderSection = (title, items, isStaged) => {
            const h = document.createElement('div');
            h.textContent = `${title} (${items.length})`;
            h.style.padding = '8px'; h.style.fontWeight = 'bold'; h.style.borderBottom = '1px solid var(--border)';
            container.appendChild(h);

            items.forEach((item) => {
                const el = document.createElement('div');
                el.className = 'list-item';
                const s = document.createElement('span'); s.className = `file-status ${isStaged?'staged':'unstaged'}`; s.textContent = item.status;
                const f = document.createElement('span'); f.textContent = item.file;
                el.appendChild(s); el.appendChild(f);

                const itemObj = { type: 'file', el, data: item, isStaged };
                el.onclick = () => {
                    selectedItemIndex = currentListItems.indexOf(itemObj);
                    renderTabContent();
                };
                currentListItems.push(itemObj);
                if (currentListItems.length - 1 === selectedItemIndex) {
                    el.classList.add('selected');
                    const diffKey = item.file + '_' + isStaged;
                    if (lastFetchedDiff !== diffKey) {
                        lastFetchedDiff = diffKey;
                        fetchDiff(item.file, isStaged);
                    }
                }
                container.appendChild(el);
            });
        };

        if(staged.length) renderSection('Staged Changes', staged, true);
        if(unstaged.length) renderSection('Unstaged Changes', unstaged, false);

        if (staged.length > 0) {
            const cb = document.createElement('div'); cb.className = 'commit-box';
            const ta = document.createElement('textarea'); ta.id = 'commit-msg'; ta.rows = 3; ta.placeholder = "Commit message...";
            if (gitConfig.commit_template) ta.value = gitConfig.commit_template;
            const btn = document.createElement('button'); btn.textContent = 'Commit';
            btn.onclick = () => doCommit(ta.value);
            cb.appendChild(ta); cb.appendChild(btn);
            container.appendChild(cb);
        }

        if (!staged.length && !unstaged.length) {
            const empty = document.createElement('div');
            empty.style.cssText = 'padding:14px 10px; color:var(--muted); font-size:12px;';
            empty.textContent = 'No changes';
            container.appendChild(empty);
        }

    } else if (activeTab === 'branches') {
        // --- Filter bar: branch-name search + committer dropdown ---------------
        const committers = getBranchCommitters();
        // Drop a stale committer filter (e.g. after switching repos) so it can't
        // hide every branch with no way back to "all".
        if (branchCommitterFilter && !committers.includes(branchCommitterFilter)) {
            branchCommitterFilter = '';
        }

        const bar = document.createElement('div');
        bar.style.cssText = 'display:flex; gap:6px; padding:6px 8px; border-bottom:1px solid var(--border); position:sticky; top:0; background:var(--bg); z-index:1;';

        const search = document.createElement('input');
        search.id = 'branch-search-input';
        search.type = 'text';
        search.placeholder = 'ブランチ名で検索…';
        search.value = branchSearch;
        search.style.cssText = 'flex:1; min-width:0; background:var(--surface); border:1px solid var(--border); color:var(--text); padding:5px 8px; border-radius:4px; font-size:12px;';
        search.addEventListener('input', (e) => {
            const caret = e.target.selectionStart;
            branchSearch = e.target.value;
            selectedItemIndex = 0;
            renderTabContent();
            const ni = document.getElementById('branch-search-input');
            if (ni) { ni.focus(); try { ni.setSelectionRange(caret, caret); } catch (_) {} }
        });
        bar.appendChild(search);

        const sel = document.createElement('select');
        sel.id = 'branch-committer-select';
        sel.style.cssText = 'max-width:45%; background:var(--surface); border:1px solid var(--border); color:var(--text); padding:5px 6px; border-radius:4px; font-size:12px;';
        const optAll = document.createElement('option');
        optAll.value = ''; optAll.textContent = `全 committer (${committers.length})`;
        sel.appendChild(optAll);
        committers.forEach(c => {
            const o = document.createElement('option');
            o.value = c; o.textContent = c;
            if (c === branchCommitterFilter) o.selected = true;
            sel.appendChild(o);
        });
        sel.addEventListener('change', (e) => {
            branchCommitterFilter = e.target.value;
            selectedItemIndex = 0;
            renderTabContent();
            const ns = document.getElementById('branch-committer-select');
            if (ns) ns.focus();
        });
        bar.appendChild(sel);
        container.appendChild(bar);

        // --- Filtered, pre-sorted (newest-commit-first) branch rows -----------
        const q = branchSearch.trim().toLowerCase();
        const branches = parseBranchLines().filter(b => {
            if (branchCommitterFilter && b.committer !== branchCommitterFilter) return false;
            if (q && !b.shortName.toLowerCase().includes(q)) return false;
            return true;
        });

        if (branches.length === 0) {
            const empty = document.createElement('div');
            empty.style.cssText = 'padding:14px 10px; color:var(--muted); font-size:12px;';
            empty.textContent = '一致するブランチがありません';
            container.appendChild(empty);
        }

        branches.forEach((b) => {
            const name = b.shortName;
            const isHead = b.isHead;
            const isRemote = b.isRemote;
            const el = document.createElement('div'); el.className = 'list-item';

            const wtEntry = (gitData.worktrees || []).find(w => w.branch === name);

            const nameSpan = document.createElement('span');
            nameSpan.textContent = `${isHead ? '* ' : '  '}${name}${wtEntry ? ' [worktree]' : ''}`;
            if (isHead) nameSpan.style.color = 'var(--green)';
            el.appendChild(nameSpan);

            const meta = document.createElement('span');
            meta.style.cssText = 'margin-left:auto; padding-left:10px; color:var(--muted); font-size:10px; white-space:nowrap; overflow:hidden; text-overflow:ellipsis;';
            const metaBits = [b.dateRel, b.committer].filter(Boolean);
            meta.textContent = metaBits.join(' · ');
            if (b.dateIso) meta.title = `${b.dateIso}${b.committer ? '\n' + b.committer : ''}`;
            el.appendChild(meta);
            el.style.display = 'flex';
            el.style.alignItems = 'baseline';

            const itemObj = { type: 'branch', el, data: name, isHead, wtEntry, isRemote };
            el.onclick = () => { selectedItemIndex = currentListItems.indexOf(itemObj); renderTabContent(); };

            currentListItems.push(itemObj);
            if (currentListItems.length - 1 === selectedItemIndex) {
                el.classList.add('selected');
                renderBranchDetails(name, isHead, wtEntry, isRemote);
            }
            container.appendChild(el);
        });
    } else if (activeTab === 'worktrees') {
        const wts = gitData.worktrees || [];
        // Removable = everything except the main repo and bare worktrees (those
        // have no removable working directory). Prune the selection to current
        // removable paths so a just-removed worktree drops out of the count.
        const removableKeys = new Set(
            wts.filter(wt => !wt.bare && normalizePath(wt.path) !== normalizePath(currentRepo))
               .map(wt => normalizePath(wt.path)));
        for (const k of [...selectedWorktreePaths]) {
            if (!removableKeys.has(k)) selectedWorktreePaths.delete(k);
        }

        // Bulk-removal action bar. Not a list-item, so it stays out of keyboard
        // navigation; it is hidden until at least one worktree is checked.
        const bar = document.createElement('div');
        bar.id = 'wt-bulk-bar';
        renderWtBulkBar(bar);
        container.appendChild(bar);

        const addEl = document.createElement('div');
        addEl.className = 'list-item';
        addEl.style.fontWeight = 'bold';
        addEl.style.color = 'var(--accent)';
        addEl.textContent = '＋ Add Worktree';

        const addObj = { type: 'add_wt_btn', el: addEl };
        addEl.onclick = () => {
            selectedItemIndex = 0;
            renderTabContent();
        };
        currentListItems.push(addObj);

        if (selectedItemIndex === 0) {
            addEl.classList.add('selected');
            renderAddWorktreeForm();
        }
        container.appendChild(addEl);

        wts.forEach((wt, idx) => {
            const isMain = normalizePath(wt.path) === normalizePath(currentRepo);
            const removable = !wt.bare && !isMain;

            const el = document.createElement('div');
            el.className = 'list-item';

            // Per-row checkbox for multi-select removal. Omitted for the main repo
            // and bare worktrees. Clicking it must not also open the detail pane,
            // so the click is stopped from bubbling to the row's onclick.
            if (removable) {
                const cb = document.createElement('input');
                cb.type = 'checkbox';
                cb.className = 'wt-select';
                cb.setAttribute('aria-label', `${wt.path} を削除対象に選択`);
                cb.style.marginRight = '8px';
                cb.style.flexShrink = '0';
                cb.checked = selectedWorktreePaths.has(normalizePath(wt.path));
                cb.onclick = (e) => e.stopPropagation();
                cb.onchange = () => {
                    const key = normalizePath(wt.path);
                    if (cb.checked) selectedWorktreePaths.add(key);
                    else selectedWorktreePaths.delete(key);
                    renderWtBulkBar(document.getElementById('wt-bulk-bar'));
                };
                el.appendChild(cb);
            } else {
                // Spacer keeps branch names aligned with the checkboxed rows.
                const spacer = document.createElement('span');
                spacer.style.display = 'inline-block';
                spacer.style.width = '21px';
                spacer.style.flexShrink = '0';
                el.appendChild(spacer);
            }

            const branchSpan = document.createElement('span');
            branchSpan.style.color = 'var(--green)';
            branchSpan.style.fontFamily = 'monospace';
            branchSpan.textContent = wt.branch || (wt.bare ? '(bare)' : '(detached)');
            
            const pathSpan = document.createElement('span');
            pathSpan.style.color = 'var(--muted)';
            pathSpan.style.fontSize = '11px';
            pathSpan.style.marginLeft = '8px';
            pathSpan.style.overflow = 'hidden';
            pathSpan.style.textOverflow = 'ellipsis';
            pathSpan.style.whiteSpace = 'nowrap';
            const folderName = wt.path.split(/[\\/]/).filter(Boolean).pop() || wt.path;
            pathSpan.textContent = isMain ? `[Main Repo]` : `[${folderName}]`;
            
            el.appendChild(branchSpan);
            el.appendChild(pathSpan);

            const envName = (gitData.launchedByPath || {})[normalizePath(wt.path)];
            if (envName) {
                const envBadge = document.createElement('span');
                envBadge.textContent = `🚀 ${envName}`;
                envBadge.title = 'env-launcher で起動した worktree';
                envBadge.style.marginLeft = '8px';
                envBadge.style.fontSize = '10px';
                envBadge.style.color = 'var(--accent)';
                envBadge.style.border = '1px solid var(--accent)';
                envBadge.style.borderRadius = '4px';
                envBadge.style.padding = '0 5px';
                el.appendChild(envBadge);
            }

            const itemObj = { type: 'worktree', el, data: wt, isMain };
            el.onclick = () => {
                selectedItemIndex = idx + 1;
                renderTabContent();
            };
            
            currentListItems.push(itemObj);
            
            if (currentListItems.length - 1 === selectedItemIndex) {
                el.classList.add('selected');
                renderWorktreeDetails(wt, isMain);
            }
            container.appendChild(el);
        });
    } else if (activeTab === 'stash') {
        const lines = gitData.stash.trim().split('\n').filter(l=>l);
        lines.forEach(line => {
             const el = document.createElement('div'); el.className = 'list-item';
             el.textContent = line;
             const itemObj = { type: 'stash', el, data: line };
             el.onclick = () => { selectedItemIndex = currentListItems.indexOf(itemObj); renderTabContent(); };
             currentListItems.push(itemObj);
             if (currentListItems.length - 1 === selectedItemIndex) el.classList.add('selected');
             container.appendChild(el);
        });
    } else if (activeTab === 'commits') {
        const el = document.createElement('pre'); el.style.padding = '10px'; el.style.fontFamily = 'monospace';
        el.textContent = gitData.log;
        container.appendChild(el);
    }
}

async function fetchDiff(file, staged) {
    if (!currentRepo) return;
    try {
        const res = await fetch(`/api/git/diff?path=${encodeURIComponent(currentRepo)}&file=${encodeURIComponent(file)}&staged=${staged?1:0}`).then(r=>r.json());
        const right = document.getElementById('right-pane');
        right.innerHTML = '';
        if (res.output) {
            const lines = res.output.split('\n');
            lines.forEach(line => {
                const el = document.createElement('div');
                el.textContent = line;
                if (line.startsWith('+')) el.className = 'diff-add';
                else if (line.startsWith('-')) el.className = 'diff-del';
                else if (line.startsWith('@@')) el.className = 'diff-info';
                right.appendChild(el);
            });
        }
    } catch (e) { console.error(e); }
}

async function doPost(url, data) {
    if(!data.path) data.path = currentRepo;
    try {
        const res = await fetch(url, {
            method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(data)
        }).then(r=>r.json());
        if(res.error) showError(`Error: ${res.error}`);
    } catch (e) {
        showError(`Network error: ${e.message}`);
    }
    await refreshData();
}

async function doCommit(msg) {
    if(!msg) { alert('Empty commit message'); return; }
    await doPost('/api/git/commit', { message: msg });
}

