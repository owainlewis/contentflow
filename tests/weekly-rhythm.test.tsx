// @vitest-environment jsdom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useWeeklyRhythm } from "../apps/web/src/useWeeklyRhythm";
import { defaultWeeklyTargets } from "../apps/web/src/weekly-rhythm";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function deferredResponse() {
  let resolve: (response: Response) => void = () => undefined;
  const promise = new Promise<Response>((done) => { resolve = done; });
  return { promise, resolve };
}

function response(revision: number, youtube: number) {
  return Response.json({ revision, targets: { ...defaultWeeklyTargets, youtube } });
}

async function overlapSaveAndRecoveryRead() {
  const read = deferredResponse();
  const write = deferredResponse();
  let reads = 0;
  vi.stubGlobal("fetch", (_input: RequestInfo, init: RequestInit = {}) => {
    if (init.method === "PUT") return write.promise;
    reads++;
    return reads === 1 ? Promise.resolve(response(0, 1)) : read.promise;
  });
  const onSessionExpired = vi.fn();
  const hook = renderHook(() => useWeeklyRhythm("csrf", onSessionExpired, true));
  await waitFor(() => expect(hook.result.current.loaded).toBe(true));
  let saving: Promise<boolean> = Promise.resolve(false);
  act(() => { saving = hook.result.current.save({ revision: 0, targets: { ...defaultWeeklyTargets, youtube: 2 } }); });
  act(() => hook.result.current.retry());
  await waitFor(() => expect(reads).toBe(2));
  return { ...hook, read, write, saving };
}

it("does not replace a confirmed rhythm save with a delayed older recovery read", async () => {
  const { result, read, write, saving } = await overlapSaveAndRecoveryRead();
  await act(async () => { write.resolve(response(1, 2)); await saving; });
  await act(async () => { read.resolve(response(0, 1)); });
  expect(result.current.revision).toBe(1);
  expect(result.current.targets.youtube).toBe(2);
  expect(result.current.loaded).toBe(true);
  expect(result.current.pending).toBe(false);
});

it("keeps a newer observed revision when an older save response arrives last", async () => {
  const { result, read, write, saving } = await overlapSaveAndRecoveryRead();
  await act(async () => { read.resolve(response(2, 3)); });
  await act(async () => { write.resolve(response(1, 2)); await saving; });
  expect(result.current.revision).toBe(2);
  expect(result.current.targets.youtube).toBe(3);
  expect(result.current.loaded).toBe(true);
});

it("does not clear a save conflict with an earlier recovery read", async () => {
  const { result, read, write, saving } = await overlapSaveAndRecoveryRead();
  await act(async () => { write.resolve(Response.json({ error: "rhythm_revision_conflict" }, { status: 409 })); await saving; });
  await act(async () => { read.resolve(response(1, 3)); });
  expect(result.current.conflicted).toBe(true);
  expect(result.current.loaded).toBe(false);
  expect(result.current.error).toContain("changed elsewhere");
});

it("clears an earlier reload error after a confirmed save", async () => {
  const { result, read, write, saving } = await overlapSaveAndRecoveryRead();
  await act(async () => { read.resolve(Response.json({ error: "unavailable" }, { status: 503 })); });
  expect(result.current.error).toContain("could not be loaded");
  await act(async () => { write.resolve(response(1, 2)); await saving; });
  expect(result.current.error).toBe("");
  expect(result.current.loaded).toBe(true);
});
