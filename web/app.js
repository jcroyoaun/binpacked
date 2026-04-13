import { fetchJSON } from './api.js';
import { $, $$ } from './dom.js';
import { renderBins, renderDetailPanel, renderSummary } from './render.js';
import { state } from './state.js';
import { groupByNodepool } from './utils.js';

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

  state.ngVisible = new Set(groups.map(([name]) => name));
  let html = '<div class="ng-item active" data-ng="__all"><div class="ng-check"></div><span class="ng-item-label" style="font-weight:500">All nodegroups</span></div><div class="ng-divider"></div>';
  for (const [name, nodes] of groups) {
    html += '<div class="ng-item active" data-ng="'+name+'"><div class="ng-check"></div><span class="ng-item-label">'+name+'</span><span class="ng-item-count">'+nodes.length+'</span></div>';
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
    const match = [...state.ngVisible].some(value => value.toLowerCase() === pool);
    group.classList.toggle('hidden', !match);
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

async function load() {
  const app = $('#app');
  try {
    const [summary, nodesResp] = await Promise.all([
      fetchJSON('/api/v1/cluster-summary'),
      fetchJSON('/api/v1/nodes'),
    ]);

    const nodes = nodesResp.nodes || [];
    const groups = groupByNodepool(nodes);

    state.nodesData = nodes;
    app.innerHTML = renderSummary(summary) + renderBins(nodes);
    buildDropdown(groups);
    applyFilter();
  } catch (err) {
    app.innerHTML = '<div class="error">Failed to load cluster data: ' + err.message + '</div>';
  }
}

document.addEventListener('click', async event => {
  const closeBtn = event.target.closest('[data-action="close-detail"]');
  if (closeBtn) {
    closeDetail();
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

load();
setInterval(load, 30000);
