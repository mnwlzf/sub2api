import { apiClient } from "../client";

export interface TutorialContentPayload {
  content: string;
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

const tutorialAPI = {
  getTutorialContent,
  updateTutorialContent,
};

export default tutorialAPI;
