// Visual definitions for layout preset buttons.
// Each has an id (matches tileTree.presetTree), label, and mini-preview grid.

const g = (columns, rows, areas) => ({
  display: 'grid', gridTemplateColumns: columns, gridTemplateRows: rows,
  ...(areas ? { gridTemplateAreas: areas } : {}),
  gap: '1.5px', width: '22px', height: '15px',
});

const c = (area) => area ? { gridArea: area } : {};

export const PRESETS = [
  { id: '1',     label: 'Single',            miniStyle: g('1fr', '1fr'),           cells: [c()] },
  { id: '2-col', label: '2 Columns',         miniStyle: g('1fr 1fr', '1fr'),       cells: [c(), c()] },
  { id: '2-row', label: '2 Rows',            miniStyle: g('1fr', '1fr 1fr'),       cells: [c(), c()] },
  { id: '2+1',   label: '2 Left + 1 Right',  miniStyle: g('1fr 1fr', '1fr 1fr', '"a b" "c b"'),  cells: [c('a'), c('b'), c('c')] },
  { id: '1+2',   label: '1 Left + 2 Right',  miniStyle: g('1fr 1fr', '1fr 1fr', '"a b" "a c"'),  cells: [c('a'), c('b'), c('c')] },
  { id: '3-col', label: '3 Columns',         miniStyle: g('1fr 1fr 1fr', '1fr'),   cells: [c(), c(), c()] },
  { id: '2x2',   label: '2×2 Grid',          miniStyle: g('1fr 1fr', '1fr 1fr'),   cells: [c(), c(), c(), c()] },
  { id: '3x2',   label: '3×2 Grid',          miniStyle: g('1fr 1fr 1fr', '1fr 1fr'), cells: [c(), c(), c(), c(), c(), c()] },
];
