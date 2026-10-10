// Counts load failures per <img> element (not per URL, so one broken image does not
// silence the fallbacks of every other card showing it). A successful load resets it.
export function createFailCounter(maxFails: number) {
  const fails = new WeakMap<object, number>();
  return {
    /** Records a failure; true once the element failed more than maxFails times in a row. */
    fail(el: object): boolean {
      const count = (fails.get(el) || 0) + 1;
      fails.set(el, count);
      return count > maxFails;
    },
    loaded(el: object) {
      fails.delete(el);
    },
  };
}
