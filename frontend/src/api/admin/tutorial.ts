import { apiClient } from "../client";

export interface TutorialContentPayload {
  content: string;
}

export interface TutorialAssetUploadResponse {
  filename: string;
  url: string;
  markdown_snippet: string;
}

export async function getTutorialContent(slug = "user-tutorial"): Promise<TutorialContentPayload> {
  const endpoint = slug === "user-tutorial"
    ? "/admin/tutorial/content"
    : `/admin/tutorials/${encodeURIComponent(slug)}/content`;
  const { data } = await apiClient.get<string>(endpoint, {
    responseType: "text" as const,
    transformResponse: [(raw) => raw],
  });
  return { content: typeof data === "string" ? data : "" };
}

export async function updateTutorialContent(
  slug: string,
  payload: TutorialContentPayload,
): Promise<{ saved: boolean }> {
  const endpoint = slug === "user-tutorial"
    ? "/admin/tutorial/content"
    : `/admin/tutorials/${encodeURIComponent(slug)}/content`;
  const { data } = await apiClient.put<{ saved: boolean }>(
    endpoint,
    payload,
  );
  return data;
}

export async function uploadTutorialAsset(
  slug: string,
  file: File,
): Promise<TutorialAssetUploadResponse> {
  const formData = new FormData();
  formData.append("file", file);

  const endpoint = slug === "user-tutorial"
    ? "/admin/tutorial/assets"
    : `/admin/tutorials/${encodeURIComponent(slug)}/assets`;

  const { data } = await apiClient.post<TutorialAssetUploadResponse>(
    endpoint,
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
