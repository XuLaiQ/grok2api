import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { claimUpdateReload, isUpdateRunning } from "./update-state.ts";

function memoryStorage() {
  const values = new Map<string, string>();
  return { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value); } };
}

describe("update reload safety", () => {
  it("does not reload merely because a service is restarting or an update rolled back", () => {
    for (const state of ["idle", "downloading", "verifying", "extracting", "restarting", "failed", "rolled_back"]) {
      assert.equal(claimUpdateReload({ id: "job-1", state, targetVersion: "v2.0.0" }, "v2.0.0", memoryStorage()), false);
    }
  });

  it("waits for the running backend to confirm the requested version", () => {
    const job = { id: "job-1", state: "succeeded", targetVersion: "v2.0.0" };
    assert.equal(claimUpdateReload(job, "v1.0.0", memoryStorage()), false);
    assert.equal(claimUpdateReload(job, undefined, memoryStorage()), false);
    assert.equal(claimUpdateReload(job, "v2.0.0", memoryStorage()), true);
  });

  it("allows one reload per completed job across remounts and page reloads", () => {
    const storage = memoryStorage();
    const job = { id: "job-1", state: "succeeded", targetVersion: "v2.0.0" };
    assert.equal(claimUpdateReload(job, "v2.0.0", storage), true);
    assert.equal(claimUpdateReload(job, "v2.0.0", storage), false);
    assert.equal(claimUpdateReload({ ...job, id: "job-2" }, "v2.0.0", storage), true);
  });

  it("does not automatically reload if browser storage is unavailable", () => {
    const storage = { getItem: () => { throw new Error("denied"); }, setItem: () => undefined };
    assert.equal(claimUpdateReload({ id: "job-1", state: "succeeded", targetVersion: "v2.0.0" }, "v2.0.0", storage), false);
  });

  it("continues monitoring every active stage and stops treating terminal states as running", () => {
    for (const state of ["downloading", "verifying", "extracting", "restarting"]) assert.equal(isUpdateRunning({ state }), true);
    for (const state of ["idle", "succeeded", "failed", "rolled_back"]) assert.equal(isUpdateRunning({ state }), false);
    assert.equal(isUpdateRunning(undefined), false);
  });
});
