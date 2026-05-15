export interface ChatModelChoice {
  label: string
  model: string
  provider: string
  tagline: string
  taglineKey?: string
}

export const chatModelChoices: ChatModelChoice[] = [
  {
    label: 'GPT-5.5',
    model: 'gpt-5.5',
    provider: 'openai',
    tagline: 'Best overall experience',
    taglineKey: 'dashboard.chatModels.gpt55'
  },
  {
    label: 'Claude Opus 4.7',
    model: 'claude-opus-4-7',
    provider: 'openai',
    tagline: 'Deep reasoning and long writing',
    taglineKey: 'dashboard.chatModels.claudeOpus47'
  },
  {
    label: 'Claude Sonnet 4.6',
    model: 'claude-sonnet-4-6',
    provider: 'openai',
    tagline: 'Balanced daily work',
    taglineKey: 'dashboard.chatModels.claudeSonnet46'
  },
  {
    label: 'Gemini 3.1 Pro',
    model: 'gemini-3.1-pro-preview',
    provider: 'openai',
    tagline: 'Long context and multimodal work',
    taglineKey: 'dashboard.chatModels.gemini31Pro'
  },
  {
    label: 'GPT-5.4 Mini',
    model: 'gpt-5.4-mini',
    provider: 'openai',
    tagline: 'Fast and economical',
    taglineKey: 'dashboard.chatModels.gpt54Mini'
  }
]

export const defaultChatModelChoice = chatModelChoices[0]

export const imageModelChoices: ChatModelChoice[] = [
  {
    label: 'GPT Image 2',
    model: 'gpt-image-2',
    provider: 'openai',
    tagline: 'Best image quality',
    taglineKey: 'dashboard.imageModels.gptImage2'
  },
  {
    label: 'GPT Image 1.5',
    model: 'gpt-image-1.5',
    provider: 'openai',
    tagline: 'Fast iteration and image edits',
    taglineKey: 'dashboard.imageModels.gptImage15'
  },
  {
    label: 'GPT Image 1',
    model: 'gpt-image-1',
    provider: 'openai',
    tagline: 'Stable general image generation',
    taglineKey: 'dashboard.imageModels.gptImage1'
  },
  {
    label: 'DALL·E 3',
    model: 'dall-e-3',
    provider: 'openai',
    tagline: 'Classic text-to-image',
    taglineKey: 'dashboard.imageModels.dalle3'
  }
]

export const defaultImageModelChoice = imageModelChoices[0]

export const buildChatRedirectPath = (choice: ChatModelChoice) => {
  const params = new URLSearchParams()
  params.set('hfc_model', choice.model)
  params.set('hfc_provider', choice.provider)
  return `/agent/inbox?${params.toString()}`
}

export const buildChatFallbackURL = (code: string, choice: ChatModelChoice) => {
  const baseURL = import.meta.env.VITE_HFC_CHAT_URL || 'https://chat.handsfreeclub.com'
  const target = new URL(buildChatRedirectPath(choice), baseURL)
  target.searchParams.set('hfc_chat_code', code)
  return target.toString()
}

export const buildImageRedirectPath = (choice: ChatModelChoice) => {
  const params = new URLSearchParams()
  params.set('hfc_launch', 'image')
  params.set('hfc_image_model', choice.model)
  params.set('hfc_image_provider', choice.provider)
  return `/image?${params.toString()}`
}

export const buildImageFallbackURL = (code: string, choice: ChatModelChoice) => {
  const baseURL = import.meta.env.VITE_HFC_CHAT_URL || 'https://chat.handsfreeclub.com'
  const target = new URL(buildImageRedirectPath(choice), baseURL)
  target.searchParams.set('hfc_chat_code', code)
  return target.toString()
}
