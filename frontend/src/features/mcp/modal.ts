import gsap from 'gsap'
import { CustomEase } from 'gsap/CustomEase'
import { Flip } from 'gsap/Flip'

gsap.registerPlugin(Flip, CustomEase)

const DURATION = 0.7
const EASE = CustomEase.create(
  'pretty-modal',
  'M0,0 C0.305,0.206 0.116,0.567 0.3,0.8 0.394,0.921 0.491,1 1,1',
)

function getDialog(dialogId: string): HTMLDialogElement | null {
  const el = document.getElementById(dialogId)
  return el instanceof HTMLDialogElement ? el : null
}

/** Ancla el dialog debajo del boton, alineado a su borde derecho. */
function anchorToOrigin(dialog: HTMLDialogElement, origin: HTMLElement) {
  const rect = origin.getBoundingClientRect()
  const gap = 4
  dialog.style.position = 'fixed'
  dialog.style.margin = '0'
  dialog.style.top = `${rect.bottom + gap}px`
  dialog.style.right = `${Math.max(0, window.innerWidth - rect.right)}px`
  dialog.style.left = 'auto'
  dialog.style.bottom = 'auto'
}

export class PrettyModal {
  constructor() {
    this.injectStyles()
  }

  open(dialogId: string, e: Event) {
    const dialog = getDialog(dialogId)
    const origin = e.currentTarget
    if (!dialog || !(origin instanceof HTMLElement)) return

    const randomId = Math.random().toString(16).slice(2)

    dialog.dataset.flipId = randomId
    origin.dataset.flipId = randomId

    const originState = Flip.getState(origin)

    anchorToOrigin(dialog, origin)
    dialog.showModal()
    dialog.classList.add('pretty-modal-opening')

    Flip.from(originState, {
      targets: dialog,
      scale: true,
      ease: EASE,
      duration: DURATION,
      onComplete: () => {
        dialog.classList.remove('pretty-modal-opening')
        dialog.classList.add('pretty-modal-open')
      },
    })
  }

  close(dialogId: string) {
    const dialog = getDialog(dialogId)
    if (!dialog) return

    const originId = dialog.dataset.flipId
    if (!originId) {
      dialog.classList.remove('pretty-modal-open', 'pretty-modal-opening')
      dialog.close()
      return
    }

    const origin = document.querySelector(
      `[data-flip-id="${CSS.escape(originId)}"]:not(dialog)`,
    )
    if (!(origin instanceof HTMLElement)) {
      dialog.classList.remove('pretty-modal-open', 'pretty-modal-opening')
      dialog.close()
      return
    }

    const originState = Flip.getState(origin)

    dialog.classList.remove('pretty-modal-open', 'pretty-modal-opening')
    dialog.classList.add('pretty-modal-closing')

    Flip.to(originState, {
      targets: dialog,
      scale: true,
      ease: EASE,
      duration: DURATION,
      onComplete: () => {
        dialog.classList.remove('pretty-modal-closing')
        dialog.removeAttribute('style')
        dialog.close()
      },
    })
  }

  injectStyles() {
    const styles = `
      dialog::backdrop {
        background: rgb(0 0 0 / 0.28);
        opacity: 0;
      }

      dialog.pretty-modal-open::backdrop {
        opacity: 1;
      }

      dialog.pretty-modal-opening::backdrop {
        animation: pretty-modal-backdrop-in 700ms cubic-bezier(.56,.27,0,1) forwards;
      }

      /* Salida mas rapida que el Flip del panel: si van a la par, el overlay
         se queda visible cuando el modal ya se ve “cerrado”. */
      dialog.pretty-modal-closing::backdrop {
        animation: pretty-modal-backdrop-out 380ms cubic-bezier(.37,.35,0,1) forwards;
      }

      @keyframes pretty-modal-backdrop-in {
        from { opacity: 0; }
        to { opacity: 1; }
      }

      @keyframes pretty-modal-backdrop-out {
        from { opacity: 1; }
        to { opacity: 0; }
      }

      .pretty-modal-opening {
        animation: pretty-modal-opening 700ms cubic-bezier(.56,.27,0,1);
      }

      @keyframes pretty-modal-opening {
        from { opacity: 0; filter: blur(8px) }
        to { opacity: 1; filter: blur(0px) }
      }

      .pretty-modal-closing {
        animation:
          pretty-modal-closing-border-radius 500ms cubic-bezier(.56,.27,0,1),
          pretty-modal-closing-blur 500ms cubic-bezier(.37,.35,0,1),
          pretty-modal-closing-fade 700ms cubic-bezier(.56,.27,0,1);
      }

      @keyframes pretty-modal-closing-border-radius {
        to { border-radius: 400px; }
      }

      @keyframes pretty-modal-closing-blur {
        0% { filter: blur(0); }
        100% { filter: blur(32px); }
      }

      @keyframes pretty-modal-closing-fade {
        from { opacity: 1; }
        to { opacity: 0; }
      }
    `

    const existing = document.getElementById('pretty-modal-styles')
    if (existing) {
      existing.textContent = styles
      return
    }

    const styleSheet = document.createElement('style')
    styleSheet.id = 'pretty-modal-styles'
    styleSheet.textContent = styles
    document.head.appendChild(styleSheet)
  }
}
