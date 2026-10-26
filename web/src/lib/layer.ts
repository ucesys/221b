/**
 * Where a menu or dialog that leaves its table cell renders: at the end of
 * the app's root, which carries the theme's colours, or of the document
 * when there is none (in a test).
 */
export function layer(): Element {
  return document.querySelector('.rk') ?? document.body
}
