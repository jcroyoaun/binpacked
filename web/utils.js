const NODEPOOL_LABELS = [
  'eks.amazonaws.com/nodegroup',
  'karpenter.sh/nodepool',
  'karpenter.sh/provisioner-name',
  'cloud.google.com/gke-nodepool',
  'kops.k8s.io/instancegroup',
  'node.kubernetes.io/nodepool',
  'agentpool',
  'nodepool',
];

export const DIST_COLORS = ['#5c3333', '#7a5a2e', '#4a5a3a', '#326CE5', '#2a7a4a'];
export const DIST_TEXT = ['#ea9a9a', '#eaca8a', '#b8d888', '#fff', '#8ae8aa'];
export const DIST_KEYS = ['0-25', '25-50', '50-75', '75-90', '90-100'];
export const DIST_LABELS = ['0-25%', '25-50%', '50-75%', '75-90%', '90-100%'];

export function getNodepool(labels) {
  if (!labels) return null;
  for (const key of NODEPOOL_LABELS) {
    if (labels[key]) return labels[key];
  }
  return null;
}

export function maxRatio(node) {
  return Math.max(node.cpu.requestRatio, node.memory.requestRatio);
}

export function groupByNodepool(nodes) {
  const groups = new Map();
  for (const node of nodes) {
    const pool = getNodepool(node.labels) || '(ungrouped)';
    if (!groups.has(pool)) groups.set(pool, []);
    groups.get(pool).push(node);
  }

  for (const [, list] of groups) {
    list.sort((a, b) => maxRatio(b) - maxRatio(a));
  }

  return [...groups.entries()].sort((a, b) => {
    if (a[0] === '(ungrouped)') return 1;
    if (b[0] === '(ungrouped)') return -1;
    return a[0].localeCompare(b[0]);
  });
}

export function ratioColor(ratio) {
  if (ratio >= 0.75) return 'var(--green)';
  if (ratio >= 0.50) return 'var(--yellow)';
  return 'var(--red)';
}

export function fmtPct(ratio) {
  return (ratio * 100).toFixed(1) + '%';
}

export function fmtCPU(millicores) {
  return millicores >= 1000 ? (millicores / 1000).toFixed(1) + ' cores' : millicores + 'm';
}

export function fmtMem(bytes) {
  const gib = bytes / (1024 * 1024 * 1024);
  return gib >= 1 ? gib.toFixed(1) + ' GiB' : (bytes / (1024 * 1024)).toFixed(0) + ' MiB';
}

export function binSizeVars(count) {
  if (count <= 2) return { '--bin-width':'200px','--bin-height':'260px','--bar-width':'36px','--bar-gap':'6px','--bar-radius':'4px','--bin-padding':'12px 10px 8px','--pct-font':'14px','--detail-font':'11px','--label-font':'12px','--bin-gap':'12px' };
  if (count <= 5) return { '--bin-width':'150px','--bin-height':'230px','--bar-width':'28px','--bar-gap':'5px','--bar-radius':'4px','--bin-padding':'10px 8px 8px','--pct-font':'13px','--detail-font':'10px','--label-font':'11px','--bin-gap':'10px' };
  if (count <= 12) return { '--bin-width':'100px','--bin-height':'190px','--bar-width':'22px','--bar-gap':'4px','--bar-radius':'3px','--bin-padding':'8px 6px 6px','--pct-font':'12px','--detail-font':'10px','--label-font':'11px','--bin-gap':'8px' };
  if (count <= 30) return { '--bin-width':'68px','--bin-height':'160px','--bar-width':'16px','--bar-gap':'3px','--bar-radius':'3px','--bin-padding':'6px 4px 5px','--pct-font':'11px','--detail-font':'9px','--label-font':'10px','--bin-gap':'6px' };
  if (count <= 60) return { '--bin-width':'52px','--bin-height':'130px','--bar-width':'12px','--bar-gap':'2px','--bar-radius':'2px','--bin-padding':'5px 3px 4px','--pct-font':'10px','--detail-font':'8px','--label-font':'9px','--bin-gap':'4px' };
  return { '--bin-width':'40px','--bin-height':'100px','--bar-width':'9px','--bar-gap':'2px','--bar-radius':'2px','--bin-padding':'4px 2px 3px','--pct-font':'9px','--detail-font':'8px','--label-font':'8px','--bin-gap':'3px' };
}

export function styleStr(vars) {
  return Object.entries(vars).map(([k, v]) => `${k}:${v}`).join(';');
}

export function poolAgg(nodes) {
  let cr = 0;
  let ca = 0;
  let mr = 0;
  let ma = 0;
  let pods = 0;

  for (const node of nodes) {
    cr += node.cpu.requested;
    ca += node.cpu.allocatable;
    mr += node.memory.requested;
    ma += node.memory.allocatable;
    pods += node.pods.count;
  }

  return `cpu ${fmtPct(ca ? cr / ca : 0)}  mem ${fmtPct(ma ? mr / ma : 0)}  ${pods} pods`;
}
