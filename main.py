# rmm/main.py
from fastapi import FastAPI, WebSocket, WebSocketDisconnect, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.templating import Jinja2Templates
import os
import logging
from typing import Dict
import asyncio

# --- Logging improvements ---
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
logger = logging.getLogger("rmm")

app = FastAPI(title="RMM Signaling Server")

# --- Improved CORS setup ---
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# --- Template setup ---
BASE_DIR = os.path.dirname(os.path.abspath(__file__))
templates = Jinja2Templates(directory=os.path.join(BASE_DIR, "templates"))

# Store connections for agents and viewers
agents: Dict[str, WebSocket] = {}

lock = asyncio.Lock()


@app.on_event("startup")
async def startup():
    asyncio.create_task(cleanup_task())


@app.get("/")
async def index(request: Request):
    """Serve the viewer HTML page."""
    return templates.TemplateResponse("index.html", {"request": request})


@app.websocket("/ws/agent/{agent_id}")
async def agent_ws(ws: WebSocket, agent_id: str):
    """Handle websocket for an agent connection (the Go client)."""
    await ws.accept()
    async with lock:
        agents[agent_id] = ws
    logger.info(f"Agent connected: {agent_id}")

    try:
        while True:
            try:
                data = await ws.receive_text()
            except (
                RuntimeError
            ) as e:  # Например, если сокет неожиданно закрылся без WebSocketDisconnect
                logger.error(f"Error receiving from {agent_id}: {e}")
                break  # Выйти из цикла, закрыть сокет
            except Exception as e:
                logger.exception(f"Unexpected error receiving from {agent_id}: {e}")
                continue  # Попробовать снова получить, если это не фатально
            # Route messages from agent to its viewer if exists
            viewer = agents.get(f"viewer:{agent_id}")
            if viewer:
                ok = await safe_send(viewer, data)
                if not ok:
                    logger.warning(f"Viewer dead, removing viewer:{agent_id}")
                    agents.pop(f"viewer:{agent_id}", None)
    except WebSocketDisconnect:
        logger.info(f"Agent disconnected: {agent_id}")
    except Exception as e:
        logger.exception(f"Error in agent socket {agent_id}: {e}")
    finally:
        async with lock:
            agents.pop(agent_id, None)


@app.websocket("/ws/viewer/{agent_id}")
async def viewer_ws(ws: WebSocket, agent_id: str):
    """Handle websocket for a viewer connection (the browser)."""
    await ws.accept()

    # Close existing viewer session for the same agent
    async with lock:
        old = agents.get(f"viewer:{agent_id}")
        if old:
            await safe_close(old)

        agents[f"viewer:{agent_id}"] = ws
        logger.info(f"Viewer connected: {agent_id}")

    try:
        while True:
            data = await ws.receive_text()
            agent = agents.get(agent_id)
            if agent:
                ok = await safe_send(agent, data)
                if not ok:
                    logger.warning(f"Agent dead, removing {agent_id}")
                    agents.pop(agent_id, None)
    except WebSocketDisconnect:
        logger.info(f"Viewer disconnected: {agent_id}")
    except Exception as e:
        logger.exception(f"Error in viewer socket {agent_id}: {e}")
    finally:
        async with lock:
            agents.pop(f"viewer:{agent_id}", None)


async def safe_close(ws: WebSocket):
    """Safely close any old WebSocket session."""
    try:
        await ws.close()
    except Exception as e:
        logger.warning(f"Error closing old WebSocket: {e}")


async def safe_send(ws: WebSocket, data: str) -> bool:
    try:
        await ws.send_text(data)
        return True
    except Exception:
        return False


async def cleanup_task():
    while True:
        await asyncio.sleep(30)

        dead = []

        for k, ws in list(agents.items()):
            try:
                await ws.send_text("ping")
            except Exception:
                dead.append(k)

        for k in dead:
            logger.warning(f"Cleaning dead ws: {k}")
            agents.pop(k, None)
