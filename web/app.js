import { fetchJSON } from './api.js';
import { $, $$ } from './dom.js';
import { renderBins, renderDetailPanel, renderSummary } from './render.js';
import { state } from './state.js';
import { groupByNodepool } from './utils.js';

let activeTooltipBin = null;
let floatingTooltip = null;

function getFloatingTooltip() {
  if (!floatingTooltip) {
    floatingTooltip = document.createElement('div');
    floatingTooltip.className = 'chart-tooltip';
    document.body.appendChild(floatingTooltip);
  }

  return floatingTooltip;
}

function positionTooltip(bin) {
  const tooltipTemplate = $('.tooltip', bin);
  if (!tooltipTemplate) return;

  const margin = 12;
  const tooltip = getFloatingTooltip();
  const maxWidth = Math.max(180, Math.min(320, window.innerWidth - (margin * 2)));

  if (activeTooltipBin && activeTooltipBin !== bin) {
    activeTooltipBin.classList.remove('tooltip-visible');
  }

  tooltip.innerHTML = tooltipTemplate.innerHTML;
  tooltip.classList.add('visible');
  tooltip.classList.remove('tooltip-below');
  tooltip.style.maxWidth = `${maxWidth}px`;
  bin.classList.add('tooltip-visible');

  const binRect = bin.getBoundingClientRect();
  const tipRect = tooltip.getBoundingClientRect();
  const availableAbove = binRect.top - margin;
  const availableBelow = window.innerHeight - binRect.bottom - margin;
  const fitsAbove = availableAbove >= tipRect.height;
  const fitsBelow = availableBelow >= tipRect.height;
  const needsBelow = !fitsAbove && (fitsBelow || availableBelow > availableAbove);

  const left = Math.max(
    margin,
    Math.min(
      binRect.left + (binRect.width / 2) - (tipRect.width / 2),
      window.innerWidth - tipRect.width - margin,
    ),
  );
  let top = needsBelow
    ? binRect.bottom + 10
    : binRect.top - tipRect.height - 10;

  top = Math.max(margin, Math.min(top, window.innerHeight - tipRect.height - margin));
  tooltip.style.left = `${left}px`;
  tooltip.style.top = `${top}px`;
  tooltip.style.setProperty(
    '--tooltip-arrow-left',
    `${Math.max(12, Math.min((binRect.left + (binRect.width / 2)) - left, tipRect.width - 12))}px`,
  );
  tooltip.classList.toggle('tooltip-below', needsBelow);
  activeTooltipBin = bin;
}

function hideTooltip(bin) {
  if (!bin) return;

  bin.classList.remove('tooltip-visible');
  if (activeTooltipBin === bin) {
    const tooltip = getFloatingTooltip();
    tooltip.classList.remove('visible', 'tooltip-below');
    tooltip.style.removeProperty('--tooltip-arrow-left');
    tooltip.innerHTML = '';
    activeTooltipBin = null;
  }
}

function closeDetail() {
  $$('.detail-panel.open').forEach(el => {
    el.classList.remove('open');
    el.innerHTML = '';
  });
  $$('.node-bin.selected').forEach(el => el.classList.remove('selected'));
  state.selectedNode = null;
  state.selectedGroupId = null;
}

function buildDropdown(groups) {
  const menu = $('#ng-menu');
  if (!menu) return;

  const names = groups.map(([name]) => name);
  if (state.ngKnown) {
    // Keep the user's filter across refreshes; pools appearing for the
    // first time default to visible.
    state.ngVisible = new Set(names.filter(name => state.ngVisible.has(name) || !state.ngKnown.has(name)));
  } else {
    state.ngVisible = new Set(names);
  }
  state.ngKnown = new Set(names);

  const allActive = names.every(name => state.ngVisible.has(name));
  let html = '<div class="ng-item'+(allActive ? ' active' : '')+'" data-ng="__all"><div class="ng-check"></div><span class="ng-item-label" style="font-weight:500">All node groups</span></div><div class="ng-divider"></div>';
  for (const [name, nodes] of groups) {
    const active = state.ngVisible.has(name);
    html += '<div class="ng-item'+(active ? ' active' : '')+'" data-ng="'+name+'"><div class="ng-check"></div><span class="ng-item-label">'+name+'</span><span class="ng-item-count">'+nodes.length+'</span></div>';
  }
  menu.innerHTML = html;
  updateNgCount();
}

function updateNgCount() {
  const countEl = $('#ng-count');
  const total = $$('.ng-item[data-ng]:not([data-ng="__all"])').length;
  if (countEl) countEl.textContent = state.ngVisible.size + '/' + total;
}

function applyFilter() {
  $$('.nodepool-group').forEach(group => {
    const pool = group.dataset.pool || '';
    // Exact match: K8s label values are case-sensitive, so "Web" and "web"
    // are distinct pools.
    group.classList.toggle('hidden', !state.ngVisible.has(pool));
  });
}

function buildNav(groups) {
  const wrap = $('#nav-pools');
  if (!wrap) return;

  wrap.innerHTML = groups.map(([name, nodes]) =>
    '<a class="nav-item nav-pool" href="#" data-pool-link="'+name+'">'+
      '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M11.99 18.54l-7.37-5.73L3 14.07l9 7 9-7-1.63-1.27-7.38 5.74zM12 16l7.36-5.73L21 9l-9-7-9 7 1.63 1.27L12 16z"/></svg>'+
      '<span class="nav-item-label" title="'+name+'">'+name+'</span>'+
      '<span class="nav-item-count">'+nodes.length+'</span>'+
    '</a>').join('');
}

function applySearch() {
  const input = $('#node-search');
  const query = (input && input.value ? input.value : '').trim().toLowerCase();
  $$('.node-bin').forEach(bin => {
    const name = (bin.dataset.node || '').toLowerCase();
    bin.classList.toggle('search-dim', !!query && !name.includes(query));
  });
  $$('.x-label').forEach(label => {
    const name = (label.dataset.nodeLabel || '').toLowerCase();
    label.classList.toggle('search-dim', !!query && !name.includes(query));
  });
}

async function openNodeDetail(nodeName, groupId, bin) {
  if (state.selectedNode === nodeName && state.selectedGroupId === groupId) {
    closeDetail();
    return;
  }

  closeDetail();
  state.selectedNode = nodeName;
  state.selectedGroupId = groupId;
  bin.classList.add('selected');

  const panel = $('#detail-' + groupId);
  if (!panel) return;

  panel.innerHTML = '<div style="padding:16px;color:var(--text-secondary);font-size:13px">Loading pods...</div>';
  panel.classList.add('open');

  const node = state.nodesData.find(item => item.name === nodeName);
  if (!node) return;

  try {
    const data = await fetchJSON('/api/v1/nodes/' + encodeURIComponent(nodeName) + '/pods');
    panel.innerHTML = renderDetailPanel(node, data.pods || []);
  } catch (err) {
    panel.innerHTML = '<div class="error" style="padding:16px">Failed: ' + err.message + '</div>';
  }
}

let loadSeq = 0;

async function load() {
  const seq = ++loadSeq;
  const app = $('#app');
  try {
    const [summary, nodesResp] = await Promise.all([
      fetchJSON('/api/v1/cluster-summary'),
      fetchJSON('/api/v1/nodes'),
    ]);
    if (seq !== loadSeq) return; // superseded by a newer load

    const nodes = nodesResp.nodes || [];
    const groups = groupByNodepool(nodes);

    if (activeTooltipBin) hideTooltip(activeTooltipBin);
    state.nodesData = nodes;
    app.innerHTML = renderSummary(summary) + renderBins(nodes);
    buildDropdown(groups);
    buildNav(groups);
    applyFilter();
    applySearch();
  } catch (err) {
    if (seq !== loadSeq) return;
    app.innerHTML = '<div class="error">Failed to load cluster data: ' + err.message + '</div>';
  }
}

document.addEventListener('click', async event => {
  const closeBtn = event.target.closest('[data-action="close-detail"]');
  if (closeBtn) {
    closeDetail();
    return;
  }

  const poolLink = event.target.closest('[data-pool-link]');
  if (poolLink) {
    event.preventDefault();
    const target = $$('.nodepool-group').find(group => group.dataset.pool === poolLink.dataset.poolLink);
    if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    return;
  }

  const toggle = event.target.closest('.nodepool-collapse');
  if (toggle) {
    toggle.closest('.nodepool-group').classList.toggle('collapsed');
    return;
  }

  const btn = $('#ng-btn');
  const menu = $('#ng-menu');
  if (btn && menu) {
    if (btn.contains(event.target)) {
      btn.classList.toggle('open');
      menu.classList.toggle('open');
      return;
    }

    const item = event.target.closest('.ng-item');
    if (item && menu.contains(item)) {
      const ng = item.dataset.ng;
      if (ng === '__all') {
        const allItems = $$('.ng-item[data-ng]:not([data-ng="__all"])');
        const allActive = allItems.every(el => el.classList.contains('active'));
        if (allActive) {
          state.ngVisible.clear();
          allItems.forEach(el => el.classList.remove('active'));
          item.classList.remove('active');
        } else {
          allItems.forEach(el => {
            el.classList.add('active');
            state.ngVisible.add(el.dataset.ng);
          });
          item.classList.add('active');
        }
      } else {
        if (state.ngVisible.has(ng)) {
          state.ngVisible.delete(ng);
          item.classList.remove('active');
        } else {
          state.ngVisible.add(ng);
          item.classList.add('active');
        }
        const allItems = $$('.ng-item[data-ng]:not([data-ng="__all"])');
        const allEl = $('.ng-item[data-ng="__all"]');
        if (allEl) {
          allEl.classList.toggle('active', allItems.every(el => el.classList.contains('active')));
        }
      }
      updateNgCount();
      applyFilter();
      return;
    }

    if (!menu.contains(event.target)) {
      btn.classList.remove('open');
      menu.classList.remove('open');
    }
  }

  const bin = event.target.closest('.node-bin');
  if (bin) {
    event.stopPropagation();
    await openNodeDetail(bin.dataset.node, bin.dataset.group, bin);
  }
});

document.addEventListener('mouseover', event => {
  const bin = event.target.closest('.node-bin');
  if (!bin || bin.contains(event.relatedTarget)) return;
  positionTooltip(bin);
});

document.addEventListener('mouseout', event => {
  const bin = event.target.closest('.node-bin');
  if (!bin || bin.contains(event.relatedTarget)) return;
  hideTooltip(bin);
});

window.addEventListener('resize', () => {
  if (activeTooltipBin) positionTooltip(activeTooltipBin);
});

window.addEventListener('scroll', () => {
  if (activeTooltipBin) positionTooltip(activeTooltipBin);
}, true);

const themeToggle = $('#theme-toggle');
if (themeToggle) {
  themeToggle.addEventListener('click', () => {
    const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem('binpacked-theme', next); } catch (err) { /* storage unavailable */ }
  });
}

const navToggle = $('#nav-toggle');
if (navToggle) {
  navToggle.addEventListener('click', () => document.body.classList.toggle('nav-hidden'));
}

const refreshBtn = $('#refresh-btn');
if (refreshBtn) {
  refreshBtn.addEventListener('click', () => load());
}

const searchInput = $('#node-search');
if (searchInput) {
  searchInput.addEventListener('input', applySearch);
}

load();
setInterval(load, 30000);
