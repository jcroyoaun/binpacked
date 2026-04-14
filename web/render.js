import {
  DIST_COLORS,
  DIST_KEYS,
  DIST_LABELS,
  DIST_TEXT,
  binSizeVars,
  fmtCPU,
  fmtMem,
  fmtPct,
  groupByNodepool,
  maxRatio,
  poolAgg,
  ratioColor,
  styleStr,
} from './utils.js';

function renderXAxisLabel(name, index) {
  return `
    <span class="x-label" title="${name}">
      <span class="x-label-id">${name}</span>
    </span>`;
}

function renderNodeBin(node, groupId, idx, showDetail) {
  const cpuPct = Math.min(node.cpu.requestRatio * 100, 100);
  const memPct = Math.min(node.memory.requestRatio * 100, 100);
  const dominant = maxRatio(node);
  const color = ratioColor(dominant);
  const delay = idx * 35;

  return `
    <div class="node-bin" data-node="${node.name}" data-group="${groupId}">
      <div class="tooltip">
        <div class="tt-title">${node.name}</div>
        <div class="tt-row"><span class="tt-lbl">CPU</span><span class="tt-val" style="color:${ratioColor(node.cpu.requestRatio)}">${fmtPct(node.cpu.requestRatio)}</span></div>
        <div class="tt-row"><span class="tt-lbl"></span><span class="tt-val" style="font-weight:400;color:var(--text-secondary)">${fmtCPU(node.cpu.requested)} / ${fmtCPU(node.cpu.allocatable)}</span></div>
        <div class="tt-row"><span class="tt-lbl">Memory</span><span class="tt-val" style="color:${ratioColor(node.memory.requestRatio)}">${fmtPct(node.memory.requestRatio)}</span></div>
        <div class="tt-row"><span class="tt-lbl"></span><span class="tt-val" style="font-weight:400;color:var(--text-secondary)">${fmtMem(node.memory.requested)} / ${fmtMem(node.memory.allocatable)}</span></div>
        <div class="tt-row"><span class="tt-lbl">Pods</span><span class="tt-val">${node.pods.count} / ${node.pods.allocatable}</span></div>
        <div class="tt-row"><span class="tt-lbl">Bottleneck</span><span class="tt-val">${node.bottleneck}</span></div>
        ${node.bestEffortPodCount > 0 ? '<div class="tt-row"><span class="tt-lbl">BestEffort</span><span class="tt-val" style="color:var(--yellow)">'+node.bestEffortPodCount+'</span></div>' : ''}
        ${node.daemonSetPodCount > 0 ? '<div class="tt-row"><span class="tt-lbl">DaemonSet</span><span class="tt-val" style="color:var(--k8s-blue-light)">'+node.daemonSetPodCount+'</span></div>' : ''}
      </div>
      <div class="bin-bars">
        <div class="bin-bar-track"><div class="bin-bar-fill cpu" style="height:${cpuPct}%;animation-delay:${delay}ms"></div></div>
        <div class="bin-bar-track"><div class="bin-bar-fill mem" style="height:${memPct}%;animation-delay:${delay+50}ms"></div></div>
      </div>
      <div class="bin-pct" style="color:${color}">${fmtPct(dominant)}</div>
      ${showDetail ? '<div class="bin-detail-line">'+fmtPct(node.cpu.requestRatio)+' / '+fmtPct(node.memory.requestRatio)+'</div>' : ''}
      ${node.bestEffortPodCount > 0 ? '<span class="be-indicator">'+node.bestEffortPodCount+' BE</span>' : ''}
    </div>`;
}

export function renderSummary(summary) {
  const total = summary.totalNodes || 1;
  return `
    <div class="k8s-card">
      <div class="k8s-card-header">Cluster Overview</div>
      <div class="k8s-card-body">
        <div class="summary-grid">
          <div class="stat-box">
            <div class="stat-label">Nodes</div>
            <div class="stat-value">${summary.totalNodes}</div>
            <div class="stat-detail">${summary.totalPods} pods running</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">CPU Packing</div>
            <div class="stat-value" style="color:${ratioColor(summary.cpu.requestRatio)}">${fmtPct(summary.cpu.requestRatio)}</div>
            <div class="stat-detail">${fmtCPU(summary.cpu.requested)} / ${fmtCPU(summary.cpu.allocatable)}</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">Memory Packing</div>
            <div class="stat-value" style="color:${ratioColor(summary.memory.requestRatio)}">${fmtPct(summary.memory.requestRatio)}</div>
            <div class="stat-detail">${fmtMem(summary.memory.requested)} / ${fmtMem(summary.memory.allocatable)}</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">Stranded CPU</div>
            <div class="stat-value" style="color:var(--yellow)">${fmtCPU(summary.strandedResources.cpuMillicores)}</div>
            <div class="stat-detail">Unrequested capacity</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">Stranded Memory</div>
            <div class="stat-value" style="color:var(--yellow)">${fmtMem(summary.strandedResources.memoryBytes)}</div>
            <div class="stat-detail">Unrequested capacity</div>
          </div>
        </div>
      </div>
    </div>
    ${total >= 8 ? `
    <div class="k8s-card">
      <div class="k8s-card-header">Packing Distribution</div>
      <div class="k8s-card-body">
        <div class="dist-bar">
          ${DIST_KEYS.map((key, i) => {
            const count = summary.distribution[key] || 0;
            if (count === 0) return '';
            const pct = (count / total) * 100;
            return '<div class="segment" style="flex:'+pct+';background:'+DIST_COLORS[i]+';color:'+DIST_TEXT[i]+'">'+count+'</div>';
          }).join('')}
        </div>
        <div class="dist-legend">
          ${DIST_KEYS.map((key, i) => {
            const count = summary.distribution[key] || 0;
            return '<span class="'+(count === 0 ? 'dim' : '')+'"><span class="dot" style="background:'+DIST_COLORS[i]+'"></span>'+DIST_LABELS[i]+'</span>';
          }).join('')}
        </div>
      </div>
    </div>` : ''}`;
}

export function renderBins(nodes) {
  const groups = groupByNodepool(nodes);
  let binIdx = 0;
  let html = '';

  for (const [poolName, poolNodes] of groups) {
    const gid = poolName.replace(/[^a-zA-Z0-9]/g, '_');
    const vars = binSizeVars(poolNodes.length);
    const showDetail = poolNodes.length <= 12;
    const binHeight = parseInt(vars['--bin-height']) || 180;

    html += `
    <div class="k8s-card nodepool-group" data-pool="${poolName.toLowerCase()}">
      <div class="nodepool-header">
        <div class="nodepool-title-area">
          <span class="nodepool-name">${poolName}</span>
          <div class="nodepool-subtitle">
            <span class="nodepool-badge">${poolNodes.length} node${poolNodes.length !== 1 ? 's' : ''}</span>
            <span>${poolAgg(poolNodes)}</span>
          </div>
        </div>
        <span class="nodepool-collapse" title="Toggle chart"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="6 9 12 15 18 9"/></svg></span>
      </div>
      <div class="bins-area">
        <div class="bins-chart-area">
          <div class="chart-legend" aria-label="Bar color legend">
            <span class="chart-legend-label">Bar colors</span>
            <span class="chart-legend-item cpu">
              <span class="swatch" style="background:var(--cpu-color)"></span>
              <span><strong>Green</strong> = CPU</span>
            </span>
            <span class="chart-legend-item mem">
              <span class="swatch" style="background:var(--mem-color)"></span>
              <span><strong>Blue</strong> = Memory</span>
            </span>
          </div>
          <div class="chart-body">
            <div class="y-axis" style="height:${binHeight + 18}px">
              <span class="y-tick">100%</span>
              <span class="y-tick">75%</span>
              <span class="y-tick">50%</span>
              <span class="y-tick">25%</span>
              <span class="y-tick">0</span>
            </div>
            <div class="chart-plot" style="${styleStr(vars)}">
              <div class="plot-grid">
                <div class="bins-row">
                  ${poolNodes.map(node => {
                    const out = renderNodeBin(node, gid, binIdx, showDetail);
                    binIdx++;
                    return out;
                  }).join('')}
                </div>
              </div>
            </div>
          </div>
          <div class="x-axis-caption">Nodes</div>
          <div class="x-axis" style="${styleStr(vars)}">
            ${poolNodes.map((node, i) => renderXAxisLabel(node.name, i)).join('')}
          </div>
        </div>
      </div>
      <div class="detail-panel" id="detail-${gid}"></div>
    </div>`;
  }

  return html;
}

export function renderDetailPanel(node, pods) {
  const cpuPct = Math.min(node.cpu.requestRatio * 100, 100);
  const memPct = Math.min(node.memory.requestRatio * 100, 100);
  let podRows = '';

  if (!pods || pods.length === 0) {
    podRows = '<tr><td colspan="5" style="text-align:center;color:var(--text-secondary);padding:16px">No pods</td></tr>';
  } else {
    podRows = pods.map(pod => '<tr>'+
      '<td style="font-family:Roboto Mono,monospace;font-size:12px">'+pod.name+(pod.isDaemonSet?'<span class="ds-badge">DS</span>':'')+'</td>'+
      '<td>'+pod.namespace+'</td>'+
      '<td style="font-family:Roboto Mono,monospace">'+fmtCPU(pod.cpu.requested)+'</td>'+
      '<td style="font-family:Roboto Mono,monospace">'+fmtMem(pod.memory.requested)+'</td>'+
      '<td><span class="qos qos-'+pod.qosClass.toLowerCase()+'">'+pod.qosClass+'</span></td>'+
      '</tr>').join('');
  }

  return `
    <div class="detail-header">
      <span class="node-title">${node.name}</span>
      <span class="node-meta">${node.pods.count} pods &middot; bottleneck: ${node.bottleneck}</span>
      <button class="close-btn" type="button" data-action="close-detail">&#x2715;</button>
    </div>
    <div class="detail-stats">
      <div class="d-stat">
        <div class="d-stat-label">CPU</div>
        <div class="d-stat-value" style="color:${ratioColor(node.cpu.requestRatio)}">${fmtPct(node.cpu.requestRatio)}</div>
        <div class="d-stat-sub">${fmtCPU(node.cpu.requested)} / ${fmtCPU(node.cpu.allocatable)}</div>
        <div class="d-stat-bar"><div class="d-stat-bar-fill" style="width:${cpuPct}%;background:var(--cpu-color)"></div></div>
      </div>
      <div class="d-stat">
        <div class="d-stat-label">Memory</div>
        <div class="d-stat-value" style="color:${ratioColor(node.memory.requestRatio)}">${fmtPct(node.memory.requestRatio)}</div>
        <div class="d-stat-sub">${fmtMem(node.memory.requested)} / ${fmtMem(node.memory.allocatable)}</div>
        <div class="d-stat-bar"><div class="d-stat-bar-fill" style="width:${memPct}%;background:var(--mem-color)"></div></div>
      </div>
      <div class="d-stat">
        <div class="d-stat-label">Pods</div>
        <div class="d-stat-value">${node.pods.count} / ${node.pods.allocatable}</div>
        <div class="d-stat-sub">${fmtPct(node.pods.ratio)} utilized</div>
      </div>
      ${node.bestEffortPodCount > 0 ? '<div class="d-stat"><div class="d-stat-label">BestEffort</div><div class="d-stat-value" style="color:var(--yellow)">'+node.bestEffortPodCount+'</div><div class="d-stat-sub">No requests</div></div>' : ''}
      ${node.daemonSetPodCount > 0 ? '<div class="d-stat"><div class="d-stat-label">DaemonSet</div><div class="d-stat-value" style="color:var(--k8s-blue-light)">'+node.daemonSetPodCount+'</div><div class="d-stat-sub">System pods</div></div>' : ''}
    </div>
    <div class="pod-table-wrap">
      <table><thead><tr><th>Pod</th><th>Namespace</th><th>CPU Req</th><th>Mem Req</th><th>QoS</th></tr></thead>
      <tbody>${podRows}</tbody></table>
    </div>`;
}
