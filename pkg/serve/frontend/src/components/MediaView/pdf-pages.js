// The pdf.js page object behind each drawn page element, so the notes can ask
// it for the text under a box. A WeakMap: a page element that goes away takes
// its entry with it.
export const pdfPages = new WeakMap();
