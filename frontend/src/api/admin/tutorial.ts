import { apiClient } from "../client";

export interface TutorialContentPayload {
  content: string;
}

export interface TutorialAssetUploadResponse {
  filename: string;
  url: string;
  markdown_snippet: string;
}

export async function getTutorialContent(): Promise<TutorialContentPayload> {
  const { data } = await apiClient.get<string>("/admin/tutorial/content", {
    responseType: "text" as const,
    transformResponse: [(raw) => raw],
  });
  return { content: typeof data === "string" ? data : "" };
}

export async function updateTutorialContent(
  payload: TutorialContentPayload,
): Promise<{ saved: boolean }> {
  const { data } = await apiClient.put<{ saved: boolean }>(
    "/admin/tutorial/content",
    payload,
  );
  return data;
}

export async function uploadTutorialAsset(
  file: File,
): Promise<TutorialAssetUploadResponse> {
  const formData = new FormData();
  formData.append("file", file);

  const { data } = await apiClient.post<TutorialAssetUploadResponse>(
    "/admin/tutorial/assets",
    formData,
    {
      headers: {
        "Content-Type": "multipart/form-data",
      },
    },
  );
  return data;
}

const tutorialAPI = {
  getTutorialContent,
  updateTutorialContent,
  uploadTutorialAsset,
};

export default tutorialAPI;
