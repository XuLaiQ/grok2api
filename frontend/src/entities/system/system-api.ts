import { apiRequest } from "@/shared/api/client";
import { createObjectDecoder, isBoolean, isOneOf, isString, type ValueValidator } from "@/shared/api/decoder";

export type SystemInfoDTO = {
  publicApiBaseURL: string;
};

const decodeSystemInfo = createObjectDecoder<SystemInfoDTO>("system info", { publicApiBaseURL: isString });

export function getSystemInfo(): Promise<SystemInfoDTO> {
  return apiRequest("/api/admin/v1/system", {}, decodeSystemInfo);
}

export type UpdateStatus = "unchecked" | "up_to_date" | "update_available" | "check_failed";

export type VersionInfoDTO = {
  currentVersion: string;
  latestVersion: string;
  updateAvailable: boolean;
  status: UpdateStatus;
  checkedAt: string | null;
  releaseUrl: string;
  releaseNotes: string;
  error: string;
  repository: string;
  canInstall: boolean;
  installDisabledReason: string;
};

const isNullableString: ValueValidator = (value) => value === null || isString(value);
const decodeVersionInfo = createObjectDecoder<VersionInfoDTO>("version info", {
  currentVersion: isString,
  latestVersion: isString,
  updateAvailable: isBoolean,
  status: isOneOf("unchecked", "up_to_date", "update_available", "check_failed"),
  checkedAt: isNullableString,
  releaseUrl: isString,
  releaseNotes: isString,
  error: isString,
  repository: isString,
  canInstall: isBoolean,
  installDisabledReason: isString,
});

export function getVersionInfo(): Promise<VersionInfoDTO> {
  return apiRequest("/api/admin/v1/system/version", { signal: AbortSignal.timeout(10_000) }, decodeVersionInfo);
}

export function checkForUpdates(): Promise<VersionInfoDTO> {
  return apiRequest("/api/admin/v1/system/update/check", { method: "POST" }, decodeVersionInfo);
}

export function saveUpdateRepository(repository: string): Promise<VersionInfoDTO> {
  return apiRequest("/api/admin/v1/system/update/config", { method: "PUT", body: { repository } }, decodeVersionInfo);
}

export type UpdateJobState = "idle" | "downloading" | "verifying" | "extracting" | "restarting" | "succeeded" | "failed" | "rolled_back";

export type UpdateJobDTO = {
  id: string;
  state: UpdateJobState;
  targetVersion: string;
  message: string;
  error: string;
  startedAt: string | null;
  updatedAt: string | null;
};

const decodeUpdateJob = createObjectDecoder<UpdateJobDTO>("update job", {
  id: isString,
  state: isOneOf("idle", "downloading", "verifying", "extracting", "restarting", "succeeded", "failed", "rolled_back"),
  targetVersion: isString,
  message: isString,
  error: isString,
  startedAt: isNullableString,
  updatedAt: isNullableString,
});

export function getUpdateJob(): Promise<UpdateJobDTO> {
  return apiRequest("/api/admin/v1/system/update/status", { signal: AbortSignal.timeout(10_000) }, decodeUpdateJob);
}

export function installUpdate(version: string): Promise<UpdateJobDTO> {
  return apiRequest("/api/admin/v1/system/update/install", { method: "POST", body: { version }, signal: AbortSignal.timeout(20_000) }, decodeUpdateJob);
}
