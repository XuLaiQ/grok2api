type JobState = { id: string; state: string; targetVersion: string };
type ReloadStorage = Pick<Storage, "getItem" | "setItem">;

export function isUpdateRunning(job: { state: string } | undefined): boolean {
  return job !== undefined && ["downloading", "verifying", "extracting", "restarting"].includes(job.state);
}

export function claimUpdateReload(job: JobState | undefined, currentVersion: string | undefined, storage: ReloadStorage): boolean {
  if (!job?.id || job.state !== "succeeded" || !job.targetVersion || currentVersion !== job.targetVersion) return false;
  const key = `grok2api:update-reloaded:${job.id}`;
  try {
    if (storage.getItem(key)) return false;
    storage.setItem(key, job.targetVersion);
    return true;
  } catch {
    // A persistent marker is required to prevent a reload loop when storage is blocked.
    return false;
  }
}
