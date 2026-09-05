import { ArrowUpRight, Check, CircleAlert, Download, Info, RefreshCw, Save, Settings } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { toast } from "sonner";

import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import type { UpdateJobDTO } from "@/entities/system/system-api";
import { isUpdateRunning } from "@/features/system/update-state";
import { useCheckForUpdates, useInstallUpdate, useUpdateJob, useUpdateReload, useUpdateRepository, useVersionInfo } from "@/features/system/use-system-updates";
import { cn } from "@/shared/lib/cn";
import { formatDateTime } from "@/shared/lib/format";

export function CurrentVersionLabel() {
  const versionQuery = useVersionInfo();
  const version = versionQuery.data?.currentVersion;
  if (!version) return null;
  return <span className="font-mono text-[10px] font-normal text-muted-foreground">{version}</span>;
}

export function VersionUpdateBanner() {
  const { t } = useTranslation();
  const versionQuery = useVersionInfo();
  const jobQuery = useUpdateJob();
  const version = versionQuery.data;
  const job = jobQuery.data;
  const running = isUpdateRunning(job);
  const failed = job?.state === "failed" || job?.state === "rolled_back";
  useUpdateReload(job);
  if (!version?.updateAvailable && !running && !failed && !jobQuery.isError) return null;

  return (
    <section aria-live="polite" className={cn("mb-5 flex min-w-0 flex-col gap-3 border-l-2 border-amber-500 bg-amber-500/10 px-4 py-3 sm:flex-row sm:items-center sm:justify-between", (failed || jobQuery.isError) && "border-destructive bg-destructive/5")}>
      <div className="flex min-w-0 items-start gap-2.5">
        {running ? <Spinner className="mt-0.5 shrink-0" /> : failed || jobQuery.isError ? <CircleAlert className="mt-0.5 size-4 shrink-0 text-destructive" /> : <Download className="mt-0.5 size-4 shrink-0 text-amber-600" />}
        <div className="min-w-0">
          <p className="break-words text-xs font-medium">
            {jobQuery.isError ? t(running ? "updates.reconnecting" : "updates.statusUnavailable") : running || failed ? t(`updates.job.${job?.state}`, { version: job?.targetVersion }) : t("updates.available", { version: version?.latestVersion })}
          </p>
          <p className="mt-1 break-words text-xs leading-5 text-muted-foreground">
            {jobQuery.isError ? t("updates.reconnectHelp") : failed ? job?.error || job?.message : running ? t("updates.targetVersion", { version: job?.targetVersion }) : t("updates.currentSummary", { version: version?.currentVersion })}
          </p>
        </div>
      </div>
      <Button type="button" variant="ghost" size="sm" className="h-8 shrink-0 self-start text-xs sm:self-auto" asChild>
        <Link to="/settings?tab=about"><Settings className="size-3.5" />{t(running || failed || jobQuery.isError ? "updates.viewProgress" : "updates.manageUpdate")}</Link>
      </Button>
    </section>
  );
}

export function VersionUpdateSection() {
  const { t, i18n } = useTranslation();
  const versionQuery = useVersionInfo();
  const checkMutation = useCheckForUpdates();
  const repositoryMutation = useUpdateRepository();
  const installMutation = useInstallUpdate();
  const jobQuery = useUpdateJob();
  const [repositoryDraft, setRepositoryDraft] = useState<string | null>(null);
  const [confirmVersion, setConfirmVersion] = useState<string | null>(null);
  const version = versionQuery.data;
  const job = jobQuery.data;
  const running = isUpdateRunning(job);
  const repositoryValue = repositoryDraft ?? version?.repository ?? "";
  const repositoryChanged = repositoryValue.trim() !== (version?.repository ?? "");
  const busy = running || installMutation.isPending || repositoryMutation.isPending || checkMutation.isPending;
  const error = errorMessage(repositoryMutation.error) || errorMessage(checkMutation.error) || version?.error || errorMessage(versionQuery.error);
  const installBlocked = busy || !version?.canInstall || !version.updateAvailable || repositoryChanged || jobQuery.isPending || jobQuery.isError || jobQuery.isFetching;

  function saveRepository() {
    repositoryMutation.mutate(repositoryValue.trim(), {
      onSuccess: () => {
        setRepositoryDraft(null);
        checkMutation.reset();
        installMutation.reset();
        toast.success(t("updates.repositorySaved"));
      },
    });
  }

  return (
    <div className="w-full space-y-8">
      <section className="space-y-3">
        <div className="flex min-h-8 items-center px-1"><h2 className="text-sm font-medium">{t("updates.title")}</h2></div>
        <div className="flex items-start gap-3 rounded-md bg-amber-500/10 px-4 py-3">
          <Info className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-300" />
          <div className="min-w-0">
            <p className="text-xs font-medium">{t("updates.noteTitle")}</p>
            <p className="mt-1 text-xs leading-5 text-muted-foreground">{t("updates.noteDescription")}</p>
          </div>
        </div>
        <div>
          <VersionField label={t("updates.repository")} description={t("updates.repositoryHelp")} controlId="update-repository">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <Input id="update-repository" autoComplete="off" spellCheck={false} placeholder="owner/repository" className="min-w-32 flex-1 font-mono" value={repositoryValue} disabled={busy || versionQuery.isPending || !version} onChange={(event) => setRepositoryDraft(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); if (repositoryChanged && !busy) saveRepository(); } }} />
              <Button type="button" variant="secondary" size="sm" disabled={!repositoryChanged || busy || !version} onClick={saveRepository}>
                {repositoryMutation.isPending ? <Spinner /> : <Save />}{t("common.save")}
              </Button>
            </div>
            {repositoryChanged ? <p className="mt-2 text-xs text-amber-700 dark:text-amber-300">{t("updates.repositoryUnsaved")}</p> : null}
          </VersionField>
          <VersionField label={t("updates.currentVersion")} description={t("updates.currentVersionHelp")}>
            <VersionValue>{version?.currentVersion || "-"}</VersionValue>
          </VersionField>
          <VersionField label={t("updates.latestVersion")} description={t("updates.latestVersionHelp")}>
            <VersionValue>{version?.latestVersion || t("updates.notChecked")}</VersionValue>
          </VersionField>
          <VersionField label={t("updates.statusLabel")} description={t("updates.statusLabelHelp")}>
            <VersionValue>
              {version?.status ? <span className={cn("size-1.5 shrink-0 rounded-full bg-muted-foreground", version.status === "up_to_date" && "bg-emerald-500", version.status === "update_available" && "bg-amber-500", version.status === "check_failed" && "bg-destructive")} /> : null}
              <span>{version ? t(`updates.status.${version.status}`) : t("common.loading")}</span>
            </VersionValue>
          </VersionField>
          <VersionField label={t("updates.checkedAt")} description={t("updates.checkedAtHelp")}>
            <VersionValue>{version?.checkedAt ? formatDateTime(version.checkedAt, i18n.language) : t("updates.neverChecked")}</VersionValue>
          </VersionField>
        </div>
        {error ? <p role="alert" className="break-words text-xs leading-5 text-destructive">{error}</p> : null}
        {!version?.repository && version ? <p className="text-xs leading-5 text-muted-foreground">{t("updates.repositoryMissing")}</p> : null}
        {version?.repository && version.installDisabledReason ? <p className="break-words text-xs leading-5 text-muted-foreground">{version.installDisabledReason}</p> : null}
        <div className="flex flex-wrap items-center gap-2 pt-2">
          <Button type="button" variant="secondary" size="sm" disabled={busy || versionQuery.isPending || checkMutation.isPending || !version?.repository || repositoryChanged} onClick={() => { installMutation.reset(); checkMutation.mutate(); }}>
            {checkMutation.isPending ? <Spinner /> : <RefreshCw />}{t("updates.checkNow")}
          </Button>
          <Button type="button" size="sm" disabled={installBlocked || checkMutation.isPending} onClick={() => setConfirmVersion(version?.latestVersion ?? null)}>
            {installMutation.isPending ? <Spinner /> : <Download />}{t(job?.state === "failed" || job?.state === "rolled_back" ? "updates.retryInstall" : "updates.install")}
          </Button>
        </div>
        {installMutation.isError && !running ? <p role="alert" className="break-words text-xs leading-5 text-destructive">{t("updates.installRequestFailed")} {errorMessage(installMutation.error)}</p> : null}
      </section>

      {jobQuery.isError || (job && job.state !== "idle") ? <UpdateProgress job={job} reconnecting={jobQuery.isError} onRetry={() => void jobQuery.refetch()} retrying={jobQuery.isFetching} /> : null}

      {version?.releaseNotes || version?.releaseUrl ? (
        <section className="space-y-3">
          <div className="flex min-h-8 flex-wrap items-center justify-between gap-3 px-1">
            <div className="min-w-0">
              <h3 className="text-sm font-medium">{t("updates.releaseNotes")}</h3>
              <p className="mt-1 text-xs leading-5 text-muted-foreground">{t("updates.releaseNotesHelp")}</p>
            </div>
            {version.releaseUrl ? (
              <Button type="button" variant="secondary" size="sm" asChild>
                <a href={version.releaseUrl} target="_blank" rel="noreferrer">{t("updates.openRelease")}<ArrowUpRight /></a>
              </Button>
            ) : null}
          </div>
          <p className="min-w-0 whitespace-pre-wrap break-words border-t px-1 pt-3 text-xs leading-5 text-muted-foreground">{version.releaseNotes || t("updates.noReleaseNotes")}</p>
        </section>
      ) : null}

      <AlertDialog open={confirmVersion !== null} onOpenChange={(open) => { if (!open) setConfirmVersion(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="break-words">{t("updates.confirmTitle", { version: confirmVersion })}</AlertDialogTitle>
            <AlertDialogDescription className="whitespace-pre-line">{t("updates.confirmDescription", { repository: version?.repository })}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel type="button">{t("common.cancel")}</AlertDialogCancel>
            <Button type="button" size="sm" disabled={installBlocked || checkMutation.isPending || confirmVersion !== version?.latestVersion} onClick={() => {
              if (!confirmVersion || installBlocked) return;
              installMutation.mutate(confirmVersion);
              setConfirmVersion(null);
            }}><Download />{t("updates.confirmInstall")}</Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

const updateStages = ["downloading", "verifying", "extracting", "restarting", "succeeded"] as const;

function UpdateProgress({ job, reconnecting, onRetry, retrying }: { job: UpdateJobDTO | undefined; reconnecting: boolean; onRetry: () => void; retrying: boolean }) {
  const { t, i18n } = useTranslation();
  const running = isUpdateRunning(job);
  const failed = job?.state === "failed" || job?.state === "rolled_back";
  const stageIndex = updateStages.findIndex((stage) => stage === job?.state);
  return (
    <section aria-live="polite" aria-busy={running} className="space-y-3 border-t pt-5">
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2.5">
          {running ? <Spinner className="mt-0.5 shrink-0" /> : failed || reconnecting ? <CircleAlert className="mt-0.5 size-4 shrink-0 text-destructive" /> : <Check className="mt-0.5 size-4 shrink-0 text-emerald-600" />}
          <div className="min-w-0">
            <h3 className="break-words text-sm font-medium">{reconnecting ? t(running ? "updates.reconnecting" : "updates.statusUnavailable") : t(`updates.job.${job?.state}`, { version: job?.targetVersion })}</h3>
            {job?.targetVersion ? <p className="mt-1 break-words text-xs text-muted-foreground">{t("updates.targetVersion", { version: job.targetVersion })}</p> : null}
          </div>
        </div>
        {reconnecting ? <Button type="button" variant="secondary" size="sm" disabled={retrying} onClick={onRetry}>{retrying ? <Spinner /> : <RefreshCw />}{t("common.retry")}</Button> : null}
      </div>
      {running || job?.state === "succeeded" ? (
        <ol className="grid grid-cols-2 gap-x-4 gap-y-3 pt-1 sm:grid-cols-5">
          {updateStages.map((stage, index) => (
            <li key={stage} className={cn("min-w-0 border-t-2 border-border pt-2 text-xs text-muted-foreground", index < stageIndex && "border-emerald-500 text-foreground", index === stageIndex && "border-primary text-foreground")} aria-current={index === stageIndex ? "step" : undefined}>
              {t(`updates.stage.${stage}`)}
            </li>
          ))}
        </ol>
      ) : null}
      {reconnecting ? <p className="text-xs leading-5 text-muted-foreground">{t("updates.reconnectHelp")}</p> : null}
      {job?.error || job?.message ? <p className={cn("whitespace-pre-wrap break-words text-xs leading-5 text-muted-foreground", failed && "text-destructive")}>{job.error || job.message}</p> : null}
      {job?.state === "rolled_back" ? <p className="text-xs leading-5 text-muted-foreground">{t("updates.rollbackHelp")}</p> : null}
      {job?.updatedAt ? <p className="text-[11px] text-muted-foreground">{t("updates.progressUpdatedAt", { time: formatDateTime(job.updatedAt, i18n.language) })}</p> : null}
    </section>
  );
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "";
}

function VersionField({ label, description, controlId, children }: { label: string; description: string; controlId?: string; children: ReactNode }) {
  return (
    <div className="min-w-0 py-4">
      <div className="grid min-w-0 gap-2.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] sm:items-center sm:gap-8">
        <div className="min-w-0">
          {controlId ? <label htmlFor={controlId} className="text-xs font-medium">{label}</label> : <p className="text-xs font-medium">{label}</p>}
          <p className="mt-1 max-w-xl text-xs leading-5 text-muted-foreground">{description}</p>
        </div>
        <div className="min-w-0">{children}</div>
      </div>
    </div>
  );
}

function VersionValue({ children }: { children: ReactNode }) {
  return <div className="flex min-h-8 min-w-0 items-center gap-2 break-words rounded-md bg-secondary/55 px-3 py-1 text-xs font-medium [&>span]:min-w-0">{children}</div>;
}
