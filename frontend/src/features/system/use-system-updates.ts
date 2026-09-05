import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import { checkForUpdates, getUpdateJob, getVersionInfo, installUpdate, saveUpdateRepository, type UpdateJobDTO } from "@/entities/system/system-api";
import { claimUpdateReload, isUpdateRunning } from "@/features/system/update-state";

const versionQueryKey = ["system-version"] as const;
const updateJobQueryKey = ["system-update-job"] as const;

export function useVersionInfo() {
  return useQuery({ queryKey: versionQueryKey, queryFn: getVersionInfo, staleTime: 60_000, retry: 1 });
}

export function useCheckForUpdates() {
  const client = useQueryClient();
  return useMutation({ mutationFn: checkForUpdates, retry: false, onSuccess: (value) => client.setQueryData(versionQueryKey, value) });
}

export function useUpdateRepository() {
  const client = useQueryClient();
  return useMutation({ mutationFn: saveUpdateRepository, retry: false, onSuccess: (value) => client.setQueryData(versionQueryKey, value) });
}

export function useUpdateJob() {
  return useQuery({
    queryKey: updateJobQueryKey,
    queryFn: getUpdateJob,
    staleTime: 1_000,
    retry: false,
    refetchInterval: (query) => query.state.error || isUpdateRunning(query.state.data) ? 2_000 : 15_000,
    refetchIntervalInBackground: true,
  });
}

export function useInstallUpdate() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: installUpdate,
    retry: false,
    onSuccess: (value) => client.setQueryData(updateJobQueryKey, value),
    // A lost response may still mean the job started. Read status before allowing another attempt.
    onSettled: () => client.invalidateQueries({ queryKey: updateJobQueryKey }),
  });
}

export function useUpdateReload(job: UpdateJobDTO | undefined) {
  const client = useQueryClient();
  const runningVersion = useQuery({
    queryKey: ["system-update-running-version", job?.id],
    queryFn: getVersionInfo,
    enabled: job?.state === "succeeded",
    staleTime: 0,
    retry: false,
    refetchInterval: (query) => query.state.data?.currentVersion === job?.targetVersion ? false : 2_000,
    refetchIntervalInBackground: true,
  });
  useEffect(() => {
    if (job?.state !== "succeeded" || !runningVersion.data) return;
    client.setQueryData(versionQueryKey, runningVersion.data);
    try {
      if (claimUpdateReload(job, runningVersion.data.currentVersion, window.sessionStorage)) window.location.reload();
    } catch {
      // Version details still update if the browser denies access to session storage.
    }
  }, [client, job, runningVersion.data]);
}
