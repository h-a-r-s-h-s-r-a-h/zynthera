import { useSyncExternalStore } from "react";

const subscribe = () => () => {};

/**
 * False while the server-rendered HTML is being hydrated, true afterwards.
 *
 * Components that show different things depending on data fetched in the
 * browser (for example "who is logged in") must render their neutral state
 * until this is true. Otherwise a query that finished before a late part of
 * the page hydrates makes the first client render differ from the server's
 * HTML, and React reports a hydration error.
 */
export function useHydrated(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => true,
    () => false,
  );
}
