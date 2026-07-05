// Shared parser for the tab-separated branch listing. Each line is
// "<refname>\t<short-name>\t<HEAD-marker>\t<committer>\t<date-relative>\t<date-iso>"
// (e.g. "refs/heads/main\tmain\t*\tDaiki\t2 hours ago\t2026-06-16 10:00:00 +0900").
// Lines are pre-sorted newest-commit-first by the backend. All helpers below
// derive from this so the tab-split parsing lives in one place.
function parseBranchLines() {
    if (!gitData.branches) return [];
    return gitData.branches.trim().split('\n').filter(l=>l)
        // Drop the refs/remotes/<remote>/HEAD symref — not a real branch.
        .filter(l => !l.split('\t')[0].endsWith('/HEAD'))
        .map(line => {
            const parts = line.split('\t');
            return {
                refname: parts[0],
                shortName: parts.length >= 2 ? parts[1] : parts[0],
                isHead: parts[2] === '*',
                isRemote: parts[0].startsWith('refs/remotes/'),
                committer: parts[3] || '',
                dateRel: parts[4] || '',
                dateIso: parts[5] || '',
            };
        });
}

// Distinct committer names across all branches, in first-appearance order
// (which, given the backend sort, is roughly most-recent-activity-first).
function getBranchCommitters() {
    const seen = [];
    parseBranchLines().forEach(b => {
        if (b.committer && !seen.includes(b.committer)) seen.push(b.committer);
    });
    return seen;
}

function getLocalBranches() {
    return parseBranchLines()
        .filter(b => b.refname.startsWith('refs/heads/'))
        .map(b => b.shortName);
}

function getAllBranchesList() {
    return parseBranchLines().map(b => b.shortName);
}

// Normalize a filesystem path for comparison: unify separators, strip a
// trailing slash, and lowercase on Windows (where paths are case-insensitive).
// git returns resolved real paths for worktrees, so this mainly guards the
// main-repo match against a trailing-slash / separator mismatch on the
// user-supplied currentRepo. (True symlink resolution isn't possible client-side.)
function normalizePath(p) {
    const s = String(p ?? '').replace(/\\/g, '/').replace(/\/+$/, '');
    return isWindows ? s.toLowerCase() : s;
}

// Small DOM builders to cut the repeated card-row / button scaffolding in the
// branch & worktree detail panes. Behaviour-preserving: callers still supply
// the (already-escaped) value markup and assign the click handlers.
const LABEL_SPAN_STYLE = 'color: var(--muted); font-size: 11px; display: block; margin-bottom: 2px; text-transform: uppercase;';
function makeInfoRow(label, valueHtml) {
    const row = document.createElement('div');
    row.innerHTML = `<span style="${LABEL_SPAN_STYLE}">${label}</span>${valueHtml}`;
    return row;
}

const BTN_STYLES = {
    primary: 'background: var(--accent); color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-weight: 500;',
    secondary: 'background: var(--surface); border: 1px solid var(--border); color: var(--text); padding: 8px 16px; border-radius: 4px; cursor: pointer; font-weight: 500;',
    danger: 'background: transparent; border: 1px solid var(--red); color: var(--red); padding: 8px 16px; border-radius: 4px; cursor: pointer; font-weight: 500;',
};
function makeButton(text, variant = 'primary') {
    const btn = document.createElement('button');
    btn.textContent = text;
    btn.style.cssText = BTN_STYLES[variant] || BTN_STYLES.primary;
    return btn;
}

function renderBranchDetails(branchName, isHead, wtEntry, isRemote) {
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
    title.textContent = 'Branch Details';
    container.appendChild(title);
    
    const card = document.createElement('div');
    card.style.background = 'var(--surface)';
    card.style.border = '1px solid var(--border)';
    card.style.borderRadius = '6px';
    card.style.padding = '16px';
    card.style.display = 'flex';
    card.style.flexDirection = 'column';
    card.style.gap = '12px';
    
    card.appendChild(makeInfoRow('Branch Name',
        `<span style="font-family: monospace; font-size: 15px; font-weight: 600; color: var(--green);">${escapeHtml(branchName)}</span>`));

    let statusText = 'Inactive branch';
    if (isHead) statusText = 'Active (HEAD) branch in the main repository';
    else if (wtEntry) statusText = `Checked out as a worktree at <code style="background: var(--bg); padding: 2px 4px; border-radius: 4px; font-size: 12px;">${escapeHtml(wtEntry.path)}</code>`;

    card.appendChild(makeInfoRow('Status',
        `<span style="font-size: 13px; color: var(--text);">${statusText}</span>`));

    if (wtEntry) {
        card.appendChild(makeInfoRow('Worktree Directory',
            `<span style="font-family: monospace; font-size: 13px; color: var(--blue); word-break: break-all;">${escapeHtml(wtEntry.path)}</span>`));

        if (wtEntry.head) {
            card.appendChild(makeInfoRow('Commit SHA',
                `<span style="font-family: monospace; font-size: 13px; color: var(--text);">${escapeHtml(wtEntry.head)}</span>`));
        }
    }
    
    container.appendChild(card);
    
    const btnRow = document.createElement('div');
    btnRow.style.display = 'flex';
    btnRow.style.gap = '10px';
    btnRow.style.flexWrap = 'wrap';
    
    if (!isHead && !wtEntry) {
        const btnCheckout = makeButton('Switch to Branch', 'primary');
        btnCheckout.onclick = () => {
            let bn = branchName.trim();
            const localBranches = getLocalBranches();
            if (isRemote && !localBranches.includes(bn)) {
                const parts = bn.split('/');
                if (parts.length > 1) bn = parts.slice(1).join('/');
            }
            doPost('/api/git/checkout', { branch: bn });
        };
        btnRow.appendChild(btnCheckout);
    }

    if (wtEntry) {
        const btnOpen = makeButton('Open in Editor', 'primary');
        btnOpen.onclick = () => {
            fetch(`/api/open?path=${encodeURIComponent(wtEntry.path)}`);
        };
        btnRow.appendChild(btnOpen);
    }

    if (!wtEntry && !isRemote) {
        const btnWt = makeButton('Create Worktree', 'secondary');
        btnWt.onclick = () => {
            showAddWorktreeForm(branchName);
        };
        btnRow.appendChild(btnWt);
    }

    if (!isHead && !wtEntry && !isRemote) {
        const btnDel = makeButton('Delete Branch', 'danger');
        btnDel.onclick = async () => {
            if (confirm(`Delete branch "${branchName}"?`)) {
                await doPost('/api/git/branch/delete', { branch: branchName, force: false });
            }
        };
        btnRow.appendChild(btnDel);

        const btnDelForce = makeButton('Force Delete', 'danger');
        btnDelForce.style.opacity = '0.7';
        btnDelForce.onclick = async () => {
            if (confirm(`Force delete branch "${branchName}"? (Unmerged changes will be lost)`)) {
                await doPost('/api/git/branch/delete', { branch: branchName, force: true });
            }
        };
        btnRow.appendChild(btnDelForce);
    }

    if (!isHead && isRemote) {
        const btnDelRemote = makeButton('Delete Remote Branch', 'danger');
        btnDelRemote.onclick = async () => {
            if (confirm(`Delete remote branch "${branchName}"?\nThis will remove the branch from the remote repository.`)) {
                await doPost('/api/git/branch/delete', { branch: branchName, remote: true });
            }
        };
        btnRow.appendChild(btnDelRemote);
    }
    
    container.appendChild(btnRow);
    right.appendChild(container);
}

