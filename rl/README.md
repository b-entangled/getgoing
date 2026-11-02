# Deep reinforcement learning with PyTorch — a knowledge base

New to RL? Start with the [reinforcement learning basics](BASICS.md), then
return here for a detailed reference to deep methods and their trade-offs.

Reinforcement learning is the machine-learning setting where nobody
hands you the answer key. An agent acts, the environment answers with a
single number, minutes late, covering everything the agent did before
it. This section is a reference for the deep version — policies and
value functions as neural networks in PyTorch — for engineers who want
to know what each algorithm is, what it costs and where it breaks.
For a practical path from simulation to a hardware deployment, see the
[physical robotics guide](ROBOTICS.md).

Every entry below carries the same five labels, in the same order, every
time: the need that justifies the method (**Why.**), a precise definition
of it (**What.**), the mechanics with tables, diagrams and PyTorch blocks
that make it runnable (**How.**), what you gain (**Advantages.**) and
what it costs in practice (**Disadvantages.**). The set is deliberately
different from the other sections of the site: read one entry and you
can skim the same five stops in every other, without relearning the map.

Read them top to bottom the first time — entry 1 defines the vocabulary
the rest reuse — and after that they work as reference. There is no Go
test suite behind this section: the PyTorch snippets are the
specification, and where a snippet and a paragraph disagree, the
snippet is the claim you can run.

Training hardware and what a GPU costs sit over in
[gpu](../gpu/README.md); the schedulers that pack many training jobs onto
one cluster sit in [distributed](../distributed/README.md); the language
features behind any Go driver that shells out to a training process are
in [concepts](../concepts/README.md) and [internals](../internals/README.md).
Engineering failure modes that catch RL projects too are catalogued in
[mistakes](../mistakes/README.md).

---

## 1. Why reinforcement learning exists — and why "deep"

**Why.** Plenty of problems hand you an answer key for every input — a
photo with a label, an email with a spam bit — and plenty do not. A bid
placed now is judged by auctions and clicks arriving over hours; a torque
applied now by whether the robot is standing forty seconds later. When
the answer is one number arriving late, you need a return instead.

**What.** Reinforcement learning is sequential decision-making under
delayed, aggregate reward. An agent observes a state s, picks an action
a, and receives a scalar reward r and a new state; the objective is the
expected discounted return E[R] = Σ_t γ^t r_t with γ in [0, 1), maximised
over policies π. "Deep" adds one thing: π_θ and the value functions
become neural networks, so the method survives an image or 10⁴ floats.

**How.** The first cut is against its two neighbours, and the difference
is the feedback, not the mathematics:

| Setup | Given to you | Feedback | Does the policy pick the data? |
|---|---|---|---|
| Supervised | (x, y) pairs, one label per input | immediate, per example | no — the test set is fixed |
| Unsupervised | x only | none; structure is the goal | no |
| Reinforcement | transitions you collect yourself | one scalar per step, judged in sum | yes — it chooses what it sees |

That last column is the whole difficulty: the agent chooses what it
sees, so the training distribution moves as the policy improves.

The formalism underneath is a Markov decision process: the tuple
(S, A, P, R, γ) — states, actions, a transition kernel P(s' | s, a), a
reward function, and the discount. "Markov" is the load-bearing word:
the next state depends only on the current one, so a state needs no
history. Everything below estimates a value V(s), a quality Q(s, a), or
a policy π(a | s).

```
supervised:  x → model → prediction → compare with label → gradient
rl:          state → policy → action → environment → reward + next state
credit:      action at t=0 → forty steps later → one reward → blame shared
```

"Deep" is the second addition and the reason this section exists.
Tabular methods keep one number per state, fine for 48 board positions
and hopeless for an 84×84×4 frame stack. A network replaces the table
with a function that generalises: states that look alike share
parameters, so a fall seen once teaches every state near it.

RL earns its place in a small set of problems: game and simulator
agents, robotic control, RLHF, ad bidding and recommendation, and
resource allocation. It loses to supervised learning the moment labels
exist — if you can collect 50,000 examples of the answer, do not spend
5,000,000 environment steps rediscovering it.

**Advantages.** It is the only family of methods that optimises a
delayed, sequential objective directly, so it can beat myopic heuristics
where any greedy rule is provably suboptimal. It needs no labels — only
a way to score a trajectory, and the same four verbs (entry 3) carry
over between games, robots, bidding and LLMs.

**Disadvantages.** It is expensive: CartPole is 50,000 steps, MuJoCo
locomotion 10⁶-10⁷, and every step is a sequential simulation you cannot
batch away. Results move by tens of percent between seeds (entry 11), so
one run proves nothing. The reward function is the real specification:
misspecify it and the agent satisfies the letter of your metric. Where
labels exist, RL is a noisier, dearer route to an answer available by
supervised learning.

---

## 2. Vocabulary: agent, environment, state, action, reward, return, policy, value

**Why.** Every paper and library reuses the same eight words, and they
are precise in a way ordinary English is not — "value" and "return" are
different objects, and confusing them makes a Bellman equation look
wrong rather than the definition look unfamiliar.

**What.** An agent chooses, an environment answers: it produces a state
s, the agent picks an action a, and the environment returns a reward r
and the next state. A policy π(a | s) is the rule for choosing, a value
V_π(s) is how good a state is under that policy, and a return G_t is the
sum of future rewards from step t onward. On-policy means the data comes
from your current policy; off-policy, from older data.

**How.** The eight terms, precisely:

| Term | Notation | Definition |
|---|---|---|
| State | s in S | everything the decision depends on (Markov: no history needed) |
| Action | a in A | a choice from the action space, discrete or continuous |
| Reward | r_t | scalar signal emitted by the environment at step t |
| Policy | `π(a|s)` | distribution over actions given a state; deterministic if it returns one action |
| Return | G_t | discounted sum of future rewards from t onward |
| Value | V_π(s) | expected return from s following π |
| Action value | Q_π(s, a) | expected return from s, taking a, then following π |
| Advantage | A(s, a) | Q_π(s, a) − V_π(s): how much better a is than the average action |

The return is what "delayed reward" means in equations:

```text
G_t = r_t + γ r_{t+1} + γ² r_{t+2} + ... = Σ_{k≥0} γ^k r_{t+k}
V_π(s) = E_π[ G_t | s_t = s ]
V_π(s)   = Σ_a π(a|s) Σ_{s',r} P(s',r|s,a) [ r + γ V_π(s') ]
Q_π(s,a) = Σ_{s',r} P(s',r|s,a) [ r + γ Σ_{a'} π(a'|s') Q_π(s',a') ]
```

γ < 1 keeps an infinite sum finite and says a reward ten steps from now
is worth less than one now — a horizon in units of 1/(1−γ), so γ = 0.99
looks about a hundred steps ahead, γ = 0.9995 about two thousand. Those
are the Bellman identities: the left side of the last two lines is a
value, the right side one step of reward plus the discounted value of
where it lands, and every algorithm here estimates, bootstraps or
enforces one of them.

Two task shapes follow from that sum. An episode task ends in a
terminal state and the return restarts at the next reset; a continuing
task never ends, which is why γ < 1 is mandatory there. The advantage
A(s, a) = Q(s, a) − V(s) is what policy gradients use: it scores an
action against the state's average, so variance drops.

On-policy and off-policy are about the data, not the name: SARSA
updates toward the action its ε-greedy policy took, Q-learning toward
max_a' Q(s', a') whatever was executed. A replay buffer makes a neural
method off-policy by definition, because the batch is old data.

**Advantages.** One vocabulary covers every method in the section, so
entry 13's table reads directly against these definitions. The Bellman
pair is a correctness check: any update you write should move V or Q
toward one of the two equations. On/off-policy is decidable from a
buffer's existence, not from an abstract.

**Disadvantages.** The terms are easy to half-know: value versus
return, advantage versus Q, state versus observation (an observation is
a state only if it is Markov). Camera observations are not Markov at
all, so entries 7 and 9 need frame stacks or recurrence to fake it.
Discounted returns hide reward-scale sensitivity: doubling every reward
changes every learning rate tuned against the loss.

---

## 3. The learning loop

**Why.** Every algorithm in this section looks different on paper —
Q-learning, REINFORCE, PPO, SAC — and almost none of that difference is
structural. They share one loop and disagree about three choices inside
it, and learning the loop once is what lets a new paper take ten minutes.

**What.** The loop is: collect experience, form a target from that
experience, update the parameters toward the target, repeat. The three
choices that separate algorithms are where the experience comes from,
what the target is (a full return, a one-step bootstrap, or a critic),
and whether the policy is represented directly or read off values.

**How.** The four verbs, spelled out. Collect: run the current policy —
or an ε-greedy variant — against the environment and store
(s, a, r, s', done) tuples. Form a target: the number the current
network should have produced, from the trajectory or one bootstrapped
step. Update: a gradient step that closes the gap. Repeat: the target
has moved, so the next batch measures something new.

The target is where the families split, and the split is a bias-variance
trade made once per algorithm. Monte Carlo uses the observed return G_t:
unbiased, because it is the definition, but every random event in the
rest of the episode enters the number, so the variance is enormous.
Temporal-difference learning bootstraps one step toward r + γV(s'): low
variance, but biased while V is wrong, and V starts wrong. TD(0) is the
dial between them:

```text
δ_t = r_t + γ V(s_{t+1}) − V(s_t)             (TD error)
V(s_t) ← V(s_t) + α δ_t                       (TD(0) update)
V(s_t) ← V(s_t) + α (G_t − V(s_t))            (Monte Carlo update)
```

α is the step size, δ_t is the error the field is named after, and every
critic in entries 6, 7 and 10 is a neural version of the second line.
Bootstrapping is what makes function approximation unstable later
(entry 10) and sample efficiency possible here — you cannot wait for the
end of a 10⁶-step task to update once.

```
interact:  policy → environment → transition (s, a, r, s') → buffer
sample:    buffer → minibatch → target network → target y
update:    y → loss → backward → optimizer step → new weights
loop:      new weights → policy → next transition → repeat
```

Those four verbs run through every algorithm below, unchanged: DQN
changes the target and adds a frozen network (entry 4); REINFORCE drops
the bootstrap (entry 5); PPO adds a ratio and a clip (entry 6); SAC adds
entropy (entry 7).

**Advantages.** Once the loop is memorised, new methods reduce to "what
is your target and where does your data come from" — two questions, both
answerable from a method section. The TD/MC dial explains why algorithms
trade sample efficiency against stability in a predictable direction,
and it gives debugging a fixed shape: if the loss falls but the reward
does not, the target is wrong.

**Disadvantages.** The loop hides the hard parts — it says "form a
target", and the target is where the field's open problems live.
Bootstrapping bias is invisible in the loss: it can fall monotonically
while the policy gets worse, because the network agrees with a target
built from its own past mistakes. Seams are where bugs sit too: a `done`
flag meaning both termination and truncation passes every loss check
while misvaluing every near-terminal state.

---

## 4. Deep Q-Networks (DQN), in depth with PyTorch ★

**Why.** Q-learning is a tabular idea — look up Q(s, a), add a bootstrap
toward r + γ max_a' Q(s', a') — and it dies the moment S is an image or
a float vector. DQN is the 2013-2015 answer: put a network in the
table's place, then spend the work making that substitution stop
exploding.

**What.** DQN learns an action-value network Q_θ(s, a) against a target
the network computes itself — y = r + γ (1 − terminal) · max_a' Q_θt(s', a')
— where θt is a lagged copy of the weights, transitions come from a
replay buffer of past experience, and acting is ε-greedy so the data
does not collapse onto one trajectory. The loss is Huber between
Q_θ(s, a) and y; the policy is implicit: argmax over the output head.

**How.** The target is learned, and that is the whole problem. In
supervised learning the labels sit still; here the target is a max over
the same network you are training, on states that network just chose to
visit. You bootstrap (the target depends on the current weights), you
approximate (the network cannot represent Q*, so error feeds into the
next target) and you correlate (consecutive transitions are nearly
identical). One step's error becomes the next step's label, and a run at
200 reward collapses to 20 with no code change.

DQN's three fixes each attack one of those. The target network freezes
θt for C steps — the original used C = 10,000 — so the target moves once
per C steps instead of every gradient step: the scaffolding holds still
while you climb. The replay buffer (10⁵-10⁶ transitions) breaks the
correlation, since a minibatch mixes unrelated states from different
points in training and reuses each transition dozens of times. ε-greedy —
random with probability ε, decaying 1.0 → 0.05 — keeps the buffer
covering states the policy does not visit.

```python
import random
from collections import deque
import gymnasium as gym
import numpy as np
import torch
import torch.nn as nn
import torch.nn.functional as F
GAMMA, BATCH, SYNC_C = 0.99, 64, 1000
class QNetwork(nn.Module):
    def __init__(self, obs_dim, act_dim):
        super().__init__()
        self.fc1 = nn.Linear(obs_dim, 128)
        self.fc2 = nn.Linear(128, 128)
        self.head = nn.Linear(128, act_dim)
    def forward(self, x):
        return self.head(F.relu(self.fc2(F.relu(self.fc1(x)))))
class ReplayBuffer:
    def __init__(self, capacity=100_000):
        self.buf = deque(maxlen=capacity)
    def push(self, s, a, r, s2, terminal):
        self.buf.append((np.asarray(s, np.float32), a, r,
                         np.asarray(s2, np.float32), float(terminal)))
    def __len__(self):
        return len(self.buf)
    def batch(self, n, device):
        s, a, r, s2, d = zip(*random.sample(self.buf, n))
        t = lambda x, dt: torch.as_tensor(np.stack(x), dtype=dt, device=device)
        return (t(s, torch.float32), t(a, torch.int64),
                t(r, torch.float32), t(s2, torch.float32), t(d, torch.float32))
def train_step(q, tgt, buf, opt, device):
    s, a, r, s2, d = buf.batch(BATCH, device)
    q_sa = q(s).gather(1, a[:, None]).squeeze(1)
    with torch.no_grad():                       # targets never grad
        y = r + GAMMA * tgt(s2).max(1).values * (1.0 - d)
    loss = F.smooth_l1_loss(q_sa, y)            # Huber, not MSE
    opt.zero_grad(set_to_none=True)
    loss.backward()
    nn.utils.clip_grad_norm_(q.parameters(), 10.0)
    opt.step()
    return loss.item()
```

The driver, over Gymnasium's CartPole, where `env.step` returns five
values (`obs, rew, term, trunc, info`) because the flags differ: `term`
means the pole fell — a real terminal state where bootstrapping must
stop — while `trunc` means the 500-step limit hit, an arbitrary cut the
target must bootstrap through. Storing `float(term)` rather than
`term or trunc` is correct; the wrong choice caps every Q-value low.

```python
device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
env = gym.make("CartPole-v1")
obs_dim, act_dim = env.observation_space.shape[0], env.action_space.n
q = QNetwork(obs_dim, act_dim).to(device)
tgt = QNetwork(obs_dim, act_dim).to(device)
tgt.load_state_dict(q.state_dict())             # frozen copy, no grads
opt = torch.optim.Adam(q.parameters(), lr=1e-3)
buf, eps = ReplayBuffer(), 1.0
obs, _ = env.reset(seed=0)
for step in range(50_000):
    if random.random() < eps:
        act = env.action_space.sample()
    else:
        with torch.no_grad():
            s_t = torch.as_tensor(obs, dtype=torch.float32, device=device)[None]
            act = int(q(s_t).argmax(1).item())
    obs2, rew, term, trunc, info = env.step(act)
    buf.push(obs, act, rew, obs2, term)
    obs = obs2
    if term or trunc:                           # fall OR time limit
        obs, _ = env.reset()
        eps = max(0.05, eps - 1e-3)             # 1.0 → 0.05 by step 950
    if len(buf) >= 1000: train_step(q, tgt, buf, opt, device)
    if step % SYNC_C == 0: tgt.load_state_dict(q.state_dict())
```

```
act:       obs → q-net → argmax or coin flip → action
step:      action → env.step → (s', r, term, trunc) → buffer push
target:    buffer sample → frozen target net → y = r + γ max Q → huber
sync:      every C steps → load_state_dict → target net → stable y
```

**Advantages.** Model-free and off-policy, so every transition is reused
dozens of times and no separate behaviour policy is needed. No policy
gradient means no variance problem and no log-probability bookkeeping.
The three fixes generalise: replay and target networks appear in nearly
every later algorithm, and CartPole reaches 500 reward within a few
thousand episodes on a laptop CPU.

**Disadvantages.** Q-learning's max is optimistic under approximation
and the bias compounds — the reason Double DQN exists. The naive form is
discrete-actions only, and one output per action scales badly past a few
hundred actions. ε-greedy is state-blind: it explores uniformly, not
where uncertainty is (entry 8). Atari scores took 50-200 million frames,
and six hyperparameters — buffer, batch, lr, γ, C, ε schedule — each
matter within roughly 2×.

---

## 5. Policy gradients: REINFORCE

**Why.** A value-based method extracts a policy by argmax over whatever
Q the network outputs, which is undefined for continuous actions —
infinitely many torques to compare — and awkward when the right answer
is genuinely stochastic. The direct route is to tune the policy itself.

**What.** REINFORCE represents π_θ(a|s) directly as a distribution over
actions, samples actions from it, and pushes up the log-probability of
the actions that turned out well. It is Monte Carlo: every update uses a
full episode's return G_t, with no bootstrap, no critic and no target
network — the gradient is an expectation over the sampled trajectories.

**How.** Start from J(θ) = E_{τ~π_θ}[ Σ_t γ^t r_t ] and move the
expectation's dependence on θ onto the integrand:

```text
∇_θ J(θ) = ∇_θ E_{τ~π_θ}[ Σ_t γ^t r_t ]
         = E_{τ~π_θ}[ Σ_t ∇_θ log π_θ(a_t|s_t) G_t ]
```

Read it as an instruction: scale the gradient of the log-probability of
what you did by how well the rest of the episode went. Positive G_t
pushes that action up, negative pulls it down, no value function needed.
Continuous actions come free — a Gaussian policy's `rsample` is
differentiable, so the same line optimises a torque with no argmax.

The estimator is unbiased and noisy, and the noise is where the
engineering goes: G_t contains every random event after step t, so its
variance grows with episode length. The fix is a baseline — subtract any
function of the state that does not depend on the action and the
estimator stays unbiased, because E[∇ log π] = 0. Subtract b = V(s) and
G_t − b is the advantage from entry 2; subtract the batch mean of
returns and you get most of the reduction for one line of code.

```python
class Policy(nn.Module):
    def __init__(self, obs_dim, act_dim, hidden=64):
        super().__init__()
        self.body = nn.Sequential(
            nn.Linear(obs_dim, hidden), nn.Tanh(), nn.Linear(hidden, hidden),
            nn.Tanh(), nn.Linear(hidden, act_dim))
    def forward(self, s):
        return torch.distributions.Categorical(logits=self.body(s))
def discounted(rewards, gamma=0.99):
    G, out = 0.0, []
    for r in reversed(rewards):
        G = r + gamma * G
        out.append(G)
    return torch.tensor(out[::-1], dtype=torch.float32)
env = gym.make("CartPole-v1")
policy = Policy(env.observation_space.shape[0], env.action_space.n)
opt = torch.optim.Adam(policy.parameters(), lr=1e-3)
for episode in range(500):
    logps, rewards = [], []
    obs, done = env.reset()[0], False
    while not done:
        dist = policy(torch.as_tensor(obs, dtype=torch.float32)[None])
        a = dist.sample()
        obs, r, term, trunc, _ = env.step(int(a))
        done = term or trunc
        logps.append(dist.log_prob(a)); rewards.append(r)
    G = discounted(rewards)
    b = G.mean()                                  # baseline: one line
    loss = -torch.stack([lp * (g - b) for lp, g in zip(logps, G)]).mean()
    opt.zero_grad()
    loss.backward()
    opt.step()
```

CartPole solves in a few hundred episodes because credit assignment is
trivial: the pole fell, so the episode was bad. Give the same code a
maze where nothing happens until the last step and it needs thousands of
episodes: every early step inherits one noisy scalar.

**Advantages.** Simple: 40 lines and one loss, with no target network,
no replay buffer and no terminal-flag subtleties. The estimator is
unbiased, so it optimises the true objective rather than a surrogate.
Continuous and stochastic policies are native, with no argmax and no
exploration noise bolted on top.

**Disadvantages.** Variance is the price: updates are correlated across
a whole episode, learning is slow, and long-horizon tasks need a learned
baseline at all. Strictly on-policy, so each sample is used once, and
the `done` used to cut returns must mean termination — truncation ends
returns that should have bootstrapped.

---

## 6. Actor–critic and PPO

**Why.** REINFORCE pays full variance because it waits for the end of
the episode to learn how good an action was, when a rough estimate of
how good a state is costs one small network. The critic exists to remove
that noise — and once both networks are in the loop, you want an update
rule that cannot throw the policy off a cliff.

**What.** An actor–critic pair learns a policy π_θ (the actor) and a
value function V_w (the critic) together. The actor updates on the
one-step advantage A = r + γV(s′) − V(s), a bootstrapped, low-variance
stand-in for G_t, while the critic is trained on the same one-step
target. PPO is A2C's update rule with a clip on how far the policy may
move per update.

**How.** The critic changes what the actor sees. Instead of G_t, the
actor uses the TD error δ = r + γV(s′) − V(s): one step of real reward
plus the critic's opinion of where you landed, minus its opinion of
where you started. It is biased — it trusts V — and much quieter,
because one transition's noise replaces an entire episode's. The critic
is a regression onto r + γV(s′): the TD(0) line of entry 3 with a
network in V's place.

On top of that, PPO asks one question: how far may the policy move per
update before the advantages computed under the old policy stop being
true? TRPO answered it with a second-order constraint and a line search;
PPO answers it with a ratio and a clamp:

```text
ρ_t(θ) = π_θ(a_t|s_t) / π_θold(a_t|s_t)
L_CLIP(θ) = E_t[ min( ρ_t A_t , clip(ρ_t, 1 − ε, 1 + ε) A_t ) ],  ε ≈ 0.2
```

When the update would move an action's probability past ±20%, the
objective flattens and gradient pressure stops — the policy cannot run
away from the data it was evaluated on. One line of tensor math under a
first-order optimiser, instead of conjugate gradients and a KL line
search: why PPO, not TRPO, became the default.

Between the advantage estimate and the clip sits GAE, the bias/variance
dial: λ interpolates between the one-step TD error (λ = 0, biased toward
the critic, low variance) and the full return (λ = 1, unbiased, high
variance). Everyone uses λ ≈ 0.95, an admission that neither end is
right.

```python
def ppo_loss(logp, logp_old, adv, v_pred, v_ret, dist, clip=0.2):
    ratio = (logp - logp_old).exp()
    surr1 = ratio * adv
    surr2 = ratio.clamp(1.0 - clip, 1.0 + clip) * adv
    policy_loss = -torch.min(surr1, surr2).mean()
    value_loss = 0.5 * (v_pred - v_ret).pow(2).mean()
    entropy = dist.entropy().mean()          # keeps exploration alive
    return policy_loss + 0.5 * value_loss - 0.01 * entropy
```

`logp_old` is detached and stored when the batch is collected, so the
ratio starts at exactly 1.0 and the clip only bites on later epochs over
the same batch — PPO reuses each rollout for 3-10 epochs, and the clip
is what makes that reuse safe.

**Advantages.** Stable: the clip bounds policy drift per update, so a
bad batch degrades one step instead of destroying the policy.
Sample-efficient among on-policy methods, because several epochs are
taken over each rollout. One objective handles discrete and continuous
actions alike, and the entropy term is a built-in exploration dial.

**Disadvantages.** Still on-policy: data is discarded once the policy
moves far, so millions of environment steps are normal and replay is off
the table. It needs many parallel environments to keep gradient steps
dense — vectorised env code, more state to seed. Clip ε and GAE λ are
two more hyperparameters, and the same settings can swing final returns
by tens of percent across tasks.

---

## 7. Continuous control: DDPG → TD3 → SAC

**Why.** Discrete action spaces let you evaluate every action and pick
the best; a 17-dimensional joint torque vector does not. You need a
policy that emits a real-valued action directly, plus a critic that
scores a specific (state, action) pair — and the first attempts at that
combination were fragile in ways worth naming one by one.

**What.** DDPG is DQN's structure with a deterministic actor: a critic
Q(s, a) trained on the same bootstrapped target, and a policy network
that outputs the action to take, explored by adding Gaussian noise
(Ornstein–Uhlenbeck in the original). TD3 keeps that skeleton and
patches three failure modes. SAC replaces determinism with a
maximum-entropy objective: highest return while staying as stochastic
as it can afford, a regulariser against the collapse of deterministic
methods.

**How.** DDPG's parts are recognisable: replay buffer, target networks
for both actor and critic, exploration by noise on the output action.
What it lacks is protection from its own critic: because the target
takes a max over Q estimates that are themselves learned,
overestimation compounds exactly as in entry 4, and a wrong critic
drags the actor toward actions it merely hopes are good.

TD3's three tricks, one line each: **twin critics** — train two Q
networks and use their minimum as the target, so one optimistic head
cannot dominate; **delayed policy updates** — update the actor every
second critic update, so the actor chases a critic that has settled;
**target policy smoothing** — add clipped noise to the action before it
enters the target critics, so no single peaky action is exploited.

SAC's move is to change the objective rather than patch the target:

```text
J(π) = E_π[ Σ_t γ^t ( r_t + α H( π(·|s_t) ) ) ]
H( π(·|s) ) = − Σ_a π(a|s) log π(a|s)     (policy entropy)
```

The entropy bonus α H rewards policies that keep their options open, so
actions with uncertain payoff still get probability mass; α is usually
tuned to hit a target entropy, trading one hyperparameter for a second
optimiser. The policy stays stochastic, which makes exploration a
property of the solution rather than a noise schedule beside it.

| Algorithm | Policy | On/off-policy | Stochastic | Typical use |
|---|---|---|---|---|
| DDPG | deterministic | off | no (+ noise at acting) | legacy continuous baselines |
| TD3 | deterministic | off | no (+ noise, smoothing) | cheap continuous benchmarks |
| SAC | stochastic, max-entropy | off | yes | robust robotics, hard exploration |
| PPO | stochastic | on | yes | RLHF, broad control, the default |

**Advantages.** Off-policy, so experience is reused through a replay
buffer and sample counts per task stay comparable to DQN rather than to
REINFORCE. TD3's three fixes are each one or two lines and transfer to
any off-policy actor–critic. SAC is typically the most robust of the
four on benchmark suites — its stochasticity doubles as exploration, so
there is no ε schedule or noise scale to tune.

**Disadvantages.** Deterministic policies need an exploration story
outside the policy — Gaussian or OU noise, whose scale silently controls
both coverage and final performance. Both critics and both targets must
be checkpointed: four networks and at least two optimisers per run.
Critic error still drives the actor: a critic that overvalues one action
gives a deterministic policy no second chances. And SAC's entropy
temperature is another loop to stabilise.

---

## 8. Exploration vs exploitation

**Why.** An agent only ever learns from data it chose to collect, so a
policy that always takes its current best action never sees the part of
the state space where the better action lives. Every algorithm above has
a hole where exploration should be, and filling it is the difference
between a demo that solves the task and a run that stalls at a local
optimum.

**What.** Exploration is the deliberate choice of actions whose value is
uncertain rather than known-good: you trade immediate reward for
coverage of the state-action space. ε-greedy takes a random action with
probability ε; softmax selection weights actions by exp(Q/τ); an entropy
bonus pays the policy for staying spread out; count-based methods add a
bonus that shrinks as N(s, a) grows. All four are the same trade in
different currencies: reward, probability, entropy, visit counts.

**How.** The simplest schedule is still the most common: act randomly
with probability ε, decaying linearly from 1.0 to 0.05 over the first
10⁴-10⁵ steps, then hold the floor. It is state-blind — the coin flip is
uniform even where three actions are obviously suicide — but it needs no
extra network and no temperature to tune.

Optimistic initialisation attacks the problem before data exists: set
every Q(s, a) high at t = 0 and greedy acting visits untried actions
first, because only their estimates are still wrongly good. Count-based
bonuses make it explicit — add β / √N(s, a) to the value, so rarely
visited states keep paying. Raw counts die in continuous spaces, so
pseudo-counts and intrinsic curiosity replace the counter with a
prediction error on learned features. That last one fails on raw
pixels: unpredictable noise is always available, so the agent chases it,
and feature-level or RND targets are the patch.

Where it breaks, concretely:

| Mode | Typical setting | What you see |
|---|---|---|
| Never explores | ε annealed in 500 steps, entropy ≈ 0 | loss falls, policy stuck on its first decent idea |
| Explores forever | ε floor 0.3, entropy never annealed | score plateaus below the greedy baseline |
| Stale optimistic data | replay keeps early random transitions | Q-value spikes on old samples (entry 4) |

**Advantages.** A good exploration scheme is often worth more than a
better network: the same DQN with ε held at 0.1 from step one solves
tasks it would otherwise never touch. Entropy-based methods make
exploration part of the objective, so there is no schedule to
desynchronise from training. Count-based bonuses are regret bounds made
cheap, and curiosity scales to tasks where enumerating good actions is
impossible.

**Disadvantages.** Every schedule is another hyperparameter with a
failure at both ends, and its effects reach the return curve only after
thousands of steps. ε-greedy wastes most random actions where the
outcome is already clear. Replay buffers keep exploratory data long
after it is representative, so value estimates stay noisy where coverage
is already good. And intrinsic rewards change the objective: an agent
can collect more curiosity than task reward, and nothing in the loss
will say so.

---

## 9. Neural networks as function approximators (the "deep" engineering)

**Why.** A table needs one row per state. Chess has on the order of
10⁴⁴ legal positions, an Atari frame stack is 84×84×4 = 28,224 numbers
before you count a joint angle, and a robot's camera never repeats
exactly twice. The replacement — a network that maps similar states to
similar values — is both why deep RL works and why it is hard to stabilise.

**What.** A function approximator replaces the lookup table with a
learned map: Q_θ(s, ·) outputs one value per action, V_θ(s) one scalar,
π_θ(s, ·) logits over actions. Generalisation is the point: two states
that differ by one pixel share almost every parameter, so experience in
one transfers to the other. That sharing is also the hazard — an error
in one state is an error in every state mapped near it.

**How.** Architecture follows the shape of the observation, and the
conventional choices are settled enough to treat as defaults. Vectors
take an MLP: two hidden layers of 128 units with ReLU, as in entry 4,
are enough for CartPole, HalfCheetah and most MuJoCo observations.
Pixels take a convolutional stack, and Atari preprocessing is the
reference recipe — downsample to 84×84 grayscale, stack four frames so
velocity is visible, clip rewards to ±1.

The output head is chosen by the object being estimated, and getting it
wrong is a shape error that still runs:

| Head | Output shape | Loss | Feeds |
|---|---|---|---|
| Q | one value per action, `|A|` | Huber against y | argmax over actions |
| V | single scalar, 1 | Huber against r + γV(s′) | advantage estimates |
| π | logits, `|A|` (or mean + std) | −log π · A | sampling, entropy |

The details that decide whether any of this trains are small and
specific. Convert numpy transitions to tensors once per step with an
explicit dtype and device, never through a DataLoader (entry 12).
Normalise rewards and observations before the first layer: a reward in
[0, 1000] beside an observation in [−0.05, 0.05] puts the loss on a
scale no fixed learning rate suits, and γ near 1 with unclipped rewards
yields targets of order 100.

And the classic bug, which costs an afternoon and a GPU every time:
computing the target as `tgt(s2)` inside the loss keeps the target
network in the autograd graph, so `loss.backward()` walks into it and
either OOMs or trains the target. Compute the target under
`torch.no_grad()`, or `.detach()` a tensor that already exists — autograd
tracks any operation on a leaf it knows. The other half of that
discipline is one device transfer per batch: `obs.to(device)` at push or
sample time, never per element.

**Advantages.** Generalisation turns sparse experience into dense
learning: one fall teaches a neighbourhood of states, not one row. The
same trunk can serve several heads, so one forward pass yields V and π
together. Preprocessing standards are settled — 84×84×4, reward
clipping, two-layer MLPs — so a new task is a config change rather than
a research project.

**Disadvantages.** Shared parameters mean correlated errors: a bad
gradient step moves many states' values at once, which is exactly the
instability entry 10 manages. Generalisation also leaks — the network
invents values for unseen states, and Q-learning uses those inventions
as targets (Double DQN, again). Output heads scale linearly with the
action space, and a 250,000-parameter MLP cannot be read for a wrong
value the way a table can.

---

## 10. Why RL training is notoriously unstable — the stability toolkit

**Why.** Two people can run the same algorithm on the same task, one
getting a curve to 500 reward and one divergence at step 40,000, and
the difference is usually a normaliser and a gradient clip. The
instability follows from three ingredients, and each standard trick
blocks one way for the run to die.

**What.** The combination is called the deadly triad, and you qualify if
you are off-policy (data from an older policy), you use a function
approximator (a network, not a table), and you bootstrap (the target
contains the network's own output). Any two are usually fine; all three
allow divergence even with linear approximation. Every critic-based
method here is inside the triad on purpose — the toolkit is what makes
the ride survivable.

**How.** Bootstrapping is the engine: the target y = r + γ max Q(s', a')
depends on Q's current weights, so a gradient step that improves Q on
the batch also moves the labels for the next batch. With a table that
feedback is bounded — one cell at a time. With a shared network a step
that fixes one state's value can shift ten thousand neighbours, and if
those neighbours feed targets back, error compounds faster than
gradient descent removes it. Off-policy then delivers a batch
distribution the targets never assumed.

The countermeasures, and what each one prevents:

| Trick | Failure it prevents | Typical setting |
|---|---|---|
| Target network | target chasing its own tail every step | hard sync every C = 1000 |
| Observation normalisation | one feature dominating the first layer | running mean/var, clip ±10 |
| Reward scaling or clipping | one 1000× reward wrecking the loss | clip ±1, or scale down |
| `clip_grad_norm_` | one exploding update after 4 hours | max_norm = 10 |
| Polyak (soft) update | a hard copy that jumps the target | τ = 0.005 every step |
| Seeding, larger batches | a win that is one lucky seed | seeds 0-9, batch 256 |

Normalisation is the one people skip and regret: observations arrive
with arbitrary scales — 10⁴ Pa of pressure beside a 0-1 valve fraction —
and the first layer cannot compensate. A running mean and variance
fixes it in a dozen lines:

```python
class RunningMeanStd:
    def __init__(self, shape=(), eps=1e-8):
        self.mean, self.var = torch.zeros(shape), torch.ones(shape)
        self.count = torch.tensor(eps)
    def update(self, x):
        bn = float(x.shape[0])
        d, tot = x.mean(0) - self.mean, self.count + bn
        self.mean = self.mean + d * (bn / tot)
        m2 = self.var * self.count + x.var(0, unbiased=False) * bn
        m2 = m2 + d.pow(2) * self.count * bn / tot
        self.var, self.count = m2 / tot, torch.tensor(tot)
    def normalize(self, x):
        return ((x - self.mean) / self.var.clamp_min(1e-8).sqrt()).clamp(-10, 10)
```

Fed rewards instead of observations, the same object is reward
normalisation — what stops a single large bonus dominating a batch.
Beside it sits `torch.nn.utils.clip_grad_norm_`, called after
`loss.backward()` and before `opt.step()`: it rescales the gradient
vector to a maximum norm, so direction survives and magnitude does not.
The soft update replaces the hard copy: `tp.lerp_(p, tau)` under
`torch.no_grad()` pulls each target parameter a fraction τ toward its
online counterpart — τ = 1 recovers entry 4's hard copy, τ = 0.005 the
usual compromise.

**Advantages.** Each trick has a named failure it prevents, so the
toolkit is a checklist rather than a prayer: loss diverges, check target
and normaliser; loss fine but reward flat, check reward scale and
terminal flags; gradients explode, check the clip. Together they make
an off-policy method trainable on a laptop, which is why DQN works at
all outside a research lab.

**Disadvantages.** Every trick adds state to the checkpoint: running
means, target networks, optimiser moments, RNG states, ε schedules — a
resume that restores the policy but not the normaliser resumes into a
different problem. The tricks interact, so ablating one often breaks
three others. Some mask the symptom: a gradient clip hides an exploding
critic that is still wrong.

---

## 11. Evaluation and the honesty problem

**Why.** Supervised learning hands you a held-out test set and a number
that means roughly what it says. RL hands you an environment your policy
has been interacting with for a million steps, a metric whose meaning
changed with every update, and seed variance large enough to explain any
result you wanted. Projects die here more often than in the algorithm.

**What.** Evaluating a policy means estimating the expected return of
the policy you have now, frozen, averaged over seeds and episode
randomness. There is no IID test set, because the data was collected by
the thing being measured: the distribution the score lives under moves
as the policy improves. The protocol substitutes for the missing rigour:
freeze the policy, run N evaluation episodes, repeat across 5-10 seeds,
report mean ± std and the median.

**How.** Seed variance is the first shock. Policy-gradient methods
routinely swing tens of percent between seeds: 512 reward in one run
and 188 in another, same task, same hyperparameters, is a normal
Tuesday. Five seeds is the practical minimum for a mean, ten for
anything you publish, and the median belongs alongside it because one
collapsed run drags a five-seed mean far more than it moves the median.

```
run 1:   200k steps → final score 512 → curve A
run 5:   200k steps → final score 188 → curve B
report:  5 seeds → mean 341, std 160 → "solved in 3 of 5 runs"
```

Reading the curve is a separate skill. Final score rewards a run that
peaked once at the end; area under the curve rewards learning early and
plateauing; time-to-threshold rewards sample efficiency. Report two of
the three, and state the protocol — episodes, whether the policy was
frozen, whether the environment was reseeded — next to the number.

Then there is reward hacking, which is not a bug in the agent: the
specification you wrote is the objective it optimises, exactly. A
simulated walker learns to lie down and inch forward, because falling
burns less of the torque reward you subtracted; an ad bidder never bids,
because your metric divided by cost rewards not spending. Each is a
correct maximum of the wrong function, and the only defence is watching
the policy, not the score.

Baselines make a number interpretable: a random policy (20-50 steps on
CartPole), a scripted controller, an imitation policy, a no-op agent. A
return of 400 is excellent against random's 22 and embarrassing against
a PID controller's 480. Real results need domain randomisation and a
stated deployment success rate, not a simulator score.

**Advantages.** A stated protocol turns an anecdote into a comparable
number, and mean ± std over seeds is the field's common currency. AUC
and time-to-threshold separate "learned fast" from "ended high", which
final score cannot. Baselines are cheap and settle most arguments. And
watching the policy finds reward hacking that every aggregate metric
misses, for ten minutes of work.

**Disadvantages.** Evaluation is expensive: 5 seeds × 100 evaluation
episodes is thousands of episodes on top of training, and honest
comparison of two runs costs twice that. The metric is partly a function
of your training loop, so it is hard to reproduce elsewhere. Seed
variance defeats single-run A/B comparisons, and a rising curve is not
evidence of the behaviour you wanted.

---

## 12. PyTorch engineering for RL, end to end ★

**Why.** The algorithm is fifty lines; the loop around it is three
hundred, and the three hundred decide whether the fifty lines ever see
a gradient. Device placement, seeding, sampling, zeroing, clipping,
updating and checkpointing are each trivial in isolation and each a
source of silent wrongness in combination.

**What.** The engineering contract for an RL training run: one device
decision made at startup and honoured everywhere; every source of
randomness seeded; replay sampled with a tensor op rather than a data
loader; an update sequence of `zero_grad(set_to_none=True)` → `backward`
→ `clip_grad_norm_` → `step`; a target updated by soft copy under
`no_grad`; and a checkpoint that captures enough to resume the run.

**How.** Device discipline means computing the device once, moving every
network at construction, and converting each numpy observation to a
tensor on that device the moment it enters the buffer — never a
per-sample transfer in the hot loop: a `.to("cuda")` per element costs
more than the forward pass it precedes. Seeding means all three sources
— `random.seed`, `np.random.seed`, `torch.manual_seed` — plus a seed for
`env.reset(seed=...)` and for the vectorised env's constructor.

Replay sampling is where a data loader is the wrong tool. A
`DataLoader` assumes an epoch over a fixed dataset: shuffle once, yield
each sample once, spawn workers, wait for the next epoch. A replay
buffer is sampled per gradient step, with replacement, from a list that
grows every environment step — `torch.randint(0, len(buf), (256,),
device=device)` is the whole operation, in one kernel.

```python
SEED = 0
random.seed(SEED)
np.random.seed(SEED)
torch.manual_seed(SEED)
device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
envs = gym.make_vecenv("CartPole-v1", num_envs=8, seed=SEED)
obs = envs.reset(seed=SEED)
def soft_update(target, net, tau=0.005):
    with torch.no_grad():
        for tp, p in zip(target.parameters(), net.parameters()):
            tp.lerp_(p, tau)
steps, updates = 0, 0
while steps < 1_000_000:
    acts = act(obs)                              # ε-greedy or sample
    obs, rew, term, trunc, info = envs.step(acts)
    buffer.push(obs, rew, term, trunc)           # one row per sub-env
    steps += envs.num_envs                       # frames, not episodes
    if buffer_ready(len(buf), 1000):
        idx = torch.randint(0, len(buf), (256,), device=device)
        batch = gather(idx)
        loss = compute_loss(batch)
        optimizer.zero_grad(set_to_none=True)
        loss.backward()
        torch.nn.utils.clip_grad_norm_(policy.parameters(), 10.0)
        optimizer.step()
        soft_update(target, policy)
        updates += 1
```

`set_to_none=True` skips the gradient fill; the clip before `step()` is
the guard against one bad batch destroying hours of work; the soft
update runs under `no_grad`, because a target parameter that joined the
graph rebuilds the problem entry 9 warned about. Checkpointing then
captures all of it, not just the policy:

```python
torch.save({
    "policy": policy.state_dict(),
    "target": target.state_dict(),
    "optimizer": optimizer.state_dict(),
    "normalizer": (rms.mean, rms.var, rms.count),
    "steps": steps, "updates": updates,
    "rng": (torch.get_rng_state(), np.random.get_state()),
}, "ckpt.pt")
```

A resume that restores the policy but not the optimiser's moments or
the normaliser's counts resumes into a different problem than the one
that paused — the loss jumps on the first batch back. Vectorised
environments cause the last confusion: 8 envs stepping together produce
8 transitions but one gradient update, so a paper reporting 1M frames
with 8 envs is 125,000 optimizer steps.

```bash
pip install torch gymnasium numpy
python train.py --env CartPole-v1 --total-steps 1000000 --seed 0
```

**Advantages.** These choices are what make a laptop run reproducible:
one device branch, one seed line, one update sequence reused across
every algorithm in the section. Tensor-op sampling removes an entire
class of concurrency bugs and a worker pool to debug. Capturing
normaliser and RNG state turns a crash into a pause, and counting
frames separately from updates makes runs comparable to papers.

**Disadvantages.** All of it is state, and state must be serialised,
versioned and restored correctly — a checkpoint that misses the
normaliser is worse than none, because it resumes confidently and wrong.
Seeding across three libraries plus the environment never gives
bit-exact reproduction: cuDNN kernels and async CUDA mean statistical
reproducibility, not identical curves. And the discipline is easy to
relax one line at a time (per-sample `.to(device)`, a DataLoader "just
for now") until the hot loop is transfer-bound.

---

## 13. Choosing an algorithm

**Why.** There are at least a dozen respectable choices, most of them
correct for some problem and wrong for yours, and the deciding factors
are almost never the ones papers lead with: sample budget, action space,
whether you may touch the environment at all, and whether you need a
deterministic policy settle the answer before any architecture question
arises.

**What.** Choosing is a lookup over four axes: the action space
(discrete or continuous), the interaction budget (millions of simulator
steps, thousands of real ones, or none), the data source (online, a
fixed dataset, or demonstrations), and whether the deployed policy must
be deterministic. Model-free estimates value or policy directly;
model-based learns P(s′|s,a) and plans; offline never sees the
environment; imitation never sees a reward.

**How.** Start with the table, because the four axes usually collapse to
one row:

| Action space | Interaction | Budget | Deterministic needed | Start with |
|---|---|---|---|---|
| discrete | online | 10⁵-10⁷ steps | no | PPO, or DQN for the simplest case |
| discrete | online | replay-friendly, tight | no | DQN + Double + target net |
| continuous | online | 10⁶+ steps | no | SAC |
| continuous | online | 10⁶, existing code | yes | TD3 |
| either | offline | no environment at all | — | conservative offline RL (CQL-style) |
| either | demonstrations exist | — | — | behaviour cloning, then fine-tune |

```
got labels:  (x, y) pairs → supervised learning → you are done
simulator:   cheap stepping + dense reward → PPO or SAC
offline:     fixed transitions only → conservative Q penalty → CQL style
demos:       expert traces → behaviour cloning → then RL fine-tune
```

The offline row deserves its own warning: with no environment to query,
the dataset's support is the boundary of what you may learn. Evaluate a
Q-function on an action the dataset never took and you are reading an
extrapolation, which Q-learning then treats as a target and amplifies —
the distribution-shift version of entry 10's triad. CQL answers by
penalising Q-values for actions the policy prefers but the dataset never
shows, and it is the only family that works without an environment.

Model-based RL — learn P(s′|s,a), then plan or synthesise data — is
rarer in deep practice than the textbook suggests: the model must be
right where the policy wants to go, which is exactly where you have not
visited, and model error compounds through a planning horizon. MBPO and
Dreamer keep the model honest — ensembles, short rollouts, latent
imagination — rather than trusting it.

Behaviour cloning is the shortcut worth taking first: clone the expert's
action distribution with supervised learning and you have a policy
before any RL runs. It fails exactly where the expert never went, but
for many products good enough imitation beats expensive optimisation.

And the answer to the most common question here: when you have labels,
use supervised learning. If a human can produce the correct action for
a state you can sample, you need a classifier, not a reward function, a
discount, five seeds and a million steps.

**Advantages.** The four axes cut a literature of dozens of methods to
one or two candidates in a minute, and each row's failures are
documented, so the first run's outcome is interpretable. Off-policy
rows let you reuse data; on-policy rows skip entry 10's machinery. The
imitation-first path de-risks a project before any RL compute is spent.

**Disadvantages.** The table assumes you can state the axes, and the
budget is unknown until a first run reports its throughput. Offline RL
is less forgiving than the row implies: conservative penalties are
another hyperparameter, and too much conservatism gives you the
dataset's average. Behaviour cloning inherits the expert's biases
permanently, and algorithms migrate between rows as code matures.

---

## 14. Where deep RL is used today, honestly

**Why.** Most of what the field ships is one of five things — RLHF,
simulated robots, bidding and recommendation, scheduling, and game
agents — and they share more than their domains suggest: a sequential
objective nobody could label directly, a way to score trajectories
cheaply, and the budget to run millions of steps. Knowing the
deployments tells you which parts of the earlier entries are load-bearing.

**What.** RLHF is the largest recent deployment: collect human
preferences between pairs of completions, train a reward model to
predict them, then fine-tune the language model with PPO against that
reward while penalising drift from the reference policy. The rest follow
the same shape with different scalars — a robot's survival, a bid's
conversion, a cluster's queueing delay — each a sequentially judged
objective evaluated by simulation or by logs.

**How.** RLHF is worth knowing in detail, because the pipeline has three
stages and each is a different kind of learning:

```
preference:  prompt → two completions → human picks a winner → pair
reward model: pairs → binary classifier → scalar per token → reward
policy update: reward + KL penalty → PPO → updated LLM → next round
```

The preference stage is supervised learning on human judgements; the
reward model is a regression head over a transformer; the policy update
is entry 6 verbatim — actor, critic, clipped ratio, GAE — with a
KL-to-reference term so the model does not drift into fluent nonsense
that merely scores high. Training reward model and policy together means
multi-billion-parameter runs on many GPUs, the hardware problem in
[gpu](../gpu/README.md), and hundreds of parallel rollout workers
against one policy version is a scheduling problem of the kind
catalogued in [distributed](../distributed/README.md).

Robotics is the other flagship: policies trained in MuJoCo, Isaac or
PyBullet and transferred to hardware, with domain randomisation —
randomised masses, friction, latencies, sensor noise — so the real robot
is just another unseen seed. Success is a deployment success rate over
trials, because the simulator's physics is what you generalise across.

Ads bidding and recommendation use RL where the objective is delayed
and sequential: a conversion arrives days after the click, and a bid
today changes tomorrow's auction. Datacenter scheduling uses it where
the state is a queue and the reward is tail latency — the same
structure as the schedulers in [distributed](../distributed/README.md),
now with a learned policy. Game agents remain the proving ground, from
Atari through Dota to grandmaster StarCraft, because the simulator is
free and the scoreboard unambiguous.

The limitations, stated plainly:

- Millions of environment steps are normal, and each step is a
  sequential simulation that does not parallelise away entirely.
- Seed sensitivity means a result is a distribution, not a number;
  entry 11's protocol is not optional ceremony.
- Reward design is the real work: the algorithm is a solved commodity,
  specifying an objective whose optimum is what you want is not, and
  specification gaming is the default under pressure.
- Safety matters more than in supervised learning: a policy that found
  a reward hack keeps acting on it.
- Reproducibility is weak: checkpoints, normalisers, environment
  versions and hardware sit between a paper's number and your run.

**Advantages.** Used where it belongs — genuinely sequential,
objectively scorable objectives with no label per step — it beats every
myopic alternative, and RLHF is the clearest example in a decade of a
method changing a product. One toolbox spans robots, ads, schedulers and
language models, so expertise transfers, and fast simulators make
iteration cheap.

**Disadvantages.** The verdict is conditional: use RL when the objective
is truly sequential and evaluable, and when you cannot write down the
correct action for a state you can reach. Do not use it when you want a
classifier, when labels exist, or when you cannot afford to be wrong
about the reward function — supervised learning wins on cost, on
stability and on your ability to explain the result. RL is a powerful,
expensive, seed-sensitive tool whose hardest component is the one no
library ships, the specification of what good means.

---

## 15. Beyond DQN: Double, Dueling, prioritised replay, distributional, Rainbow

**Why.** Plain DQN ships with three acknowledged defects: it
overestimates Q because the same network both picks and scores the
argmax, it spends a whole forward pass on states where no action
actually differs, and it treats every stored transition as equally
worth learning from. Each defect has a small, independent fix — and
each fix alone moves a benchmark only a little, which is exactly why
the field learned to combine them.

**What.** A family of drop-in upgrades to the DQN recipe. Double DQN
decouples selection from evaluation: the online network picks the next
action, the target network scores it. Dueling splits the head into
V(s) and A(s, a) with Q = V + A − mean(A), so a state keeps a value
estimate even when no action distinguishes it. Prioritised replay
samples transitions in proportion to their TD error and corrects the
sampling bias with importance weights. n-step returns bootstrap n steps
ahead before criticising, trading a little bias for a shorter credit
chain. Distributional methods (C51, QR-DQN, IQN) stop predicting the
mean return and learn the whole return distribution Z(s, a).

**How.** What each variant repairs and what it costs:

| Variant | Failure it repairs | Cost it adds |
|---|---|---|
| Double DQN | maximisation bias from pick-and-score | none — a second argmax |
| Dueling head | wasted capacity on action-blind states | one extra head |
| Prioritised replay | rare, surprising transitions drowned out | sum-tree code, IS weights |
| n-step returns | one-step targets that criticise slowly | n rewards per transition |
| Distributional | a mean hides risk and multi-modality | atom range, projection step |
| Noisy nets | ε-greedy's single global exploration dial | noise params per layer |

The Double target is two lines on top of entry 4's loop:

```python
with torch.no_grad():
    next_a = q_online(next_s).argmax(1, keepdim=True)   # online selects
    next_q = q_target(next_s).gather(1, next_a)         # target evaluates
    target = r + gamma * next_q * (1 - done)
```

C51 turns Q into a categorical distribution over fixed atoms
v_min … v_max: logits give each atom a probability, the target shifts
every atom by r + γ·v, and the result is projected back onto the fixed
support:

```text
projection: bellman shift → overlap with two neighbours → split the mass
            atoms:   v_min = −10 … v_max = 10,  N = 51 supports
```

The pieces are named Rainbow when combined (Hessel et al., 2017), and
the ablation is the lesson: each component alone adds a sliver, the
union adds a leap — because the defects compound rather than add.

**Advantages.** Every fix is orthogonal: land one, keep the others.
Double costs nothing measurable and deserves to be the default
anywhere you take a max over a network's outputs. Distributional
targets make the agent risk-aware for almost free — a bimodal return
becomes visible instead of averaged into indifference.

**Disadvantages.** The variants multiply hyperparameters: atom ranges,
β schedules, n, noise magnitudes — tuning effort that grows faster
than performance does. Prioritised replay adds machinery whose bias
correction is easy to skip and hard to notice missing. And the whole
family stays value-based: discrete actions, bootstrapped targets, and
a benchmark table where PPO from entry 6 matches it with less code on
many tasks. Learn the fixes; reach for Rainbow only when the task is
discrete, budgeted and already measured.

---

## 16. Search and world models: MCTS, AlphaZero, MuZero, Dreamer

**Why.** Model-free methods learn only by spending real interaction —
every improvement is paid for in environment steps. But a simulator is
a machine that answers "what happens if…?", and a policy that never
asks it leaves the cheapest information on the table. Search turns
that question into planning; a learned world model turns it into
imagination.

**What.** Monte Carlo tree search grows a tree from the current state:
select the most promising child under an exploration-adjusted value,
expand one node, evaluate the position with a value network, and
backpropagate that value to the root. AlphaZero couples search with
learning — the network supplies move priors that bias selection and a
value that replaces random rollouts, while self-play supplies the
training games. MuZero drops the rules entirely: it learns a latent
dynamics model and plans over states it predicts for itself, needing
no generative model of observations. Dreamer applies the same shape to
continuous control — learn a latent world model, then train an actor
entirely inside imagination, taking one real step per batch.

**How.** The search loop, one pass:

```text
select:   root → child,  UCT(s, a) = Q(s, a) + c · sqrt(ln N(s) / N(s,a))
expand:   first visit → new node, prior P(s, ·) from the network
evaluate: value v(s) from the same network — no rollouts needed
backprop: v unwinds the path; N and Q update on the way up
```

Training is a two-player loop even when the environment is not: play
games with the current network (self-play) → store (state, visit
counts, outcome) → train the policy head on the counts and the value
head on the outcome → repeat. The visit counts are a richer teacher
than the game result alone: they encode the whole tree's opinion,
move by move, as a soft target.

The three families differ in who pays for foresight:

```text
model-free:  env step → gradient      (1 data point per expensive step)
search:      tree walk → action       (deep thinking at decision time)
world model: obs → latent → imagine   (cheap steps, model must be right)
```

| Family | Sample cost | Decision cost | Where it earns its keep |
|---|---|---|---|
| Model-free (DQN, PPO) | very high | one forward pass | messy dynamics, cheap sim |
| MCTS + learned value | high (self-play) | tree of forwards | games, known rules |
| MuZero-style | very high | deep tree in latent space | games without rules given |
| Dreamer-style | moderate | one imagine batch | continuous control, pixels |

**Advantages.** Planning buys what gradient steps cannot: foresight
over a horizon, sparse rewards satisfied by looking ahead, and
decision-time compute you can dial at run time — more simulations,
better moves, without retraining. One learned model serves many
policies, unlike a policy that has memorised its own behaviour.

**Disadvantages.** The model is wrong in ways that compound: small
prediction errors accumulate along a plan until the planner confidently
walks off the data manifold, and model bias hides itself from learning
curves that still look healthy. Decision-time cost is real — a tree of
forward passes per action rules out high-rate control unless you prune
hard. And the recipe (self-play loops, tree targets, latent dynamics)
is the most elaborate in this section; it earns its place atop
leaderboards and very little elsewhere.

---

## 17. Learning from experts and people: behaviour cloning, DAgger, GAIL, DPO, GRPO

**Why.** Entry 14 ends by naming the component no library ships: the
specification of what good means. Often there is no reward function at
all — there is a human who can demonstrate the task, or humans who
will pick the better of two answers. Turning those into a policy
directly is cheaper, stabler and less arbitrary than inventing rewards
and hoping they survive contact with an optimiser.

**What.** Behaviour cloning (BC) is supervised learning on expert
trajectories: states in, expert actions out. DAgger repairs BC's blind
spot by asking the expert to label the states the learner itself
reaches. GAIL trains a discriminator to tell learner transitions from
expert ones and rewards the learner for being indistinguishable —
matching state-action occupancy rather than single steps. Preference
methods skip explicit rewards: RLHF trains a reward model on pairwise
human choices and optimises it with PPO (entry 6); DPO collapses that
pipeline into one supervised-looking loss over chosen and rejected
pairs, anchored to a reference policy by β; GRPO samples a group of
answers per prompt, scores each, and uses the within-group z-score as
the advantage — no critic at all, and no reward model when a verifier
can score the outputs.

**How.** The pipeline difference, in one glance:

```text
rlhf:  preferences → reward model → ppo optimises the learned reward
dpo:   preferences → one loss on (chosen, rejected), reference-anchored
grpo:  prompt → G samples → score each → advantage = (score − mean) / std
```

DPO's objective — supervised in shape, preference-driven in meaning:

```text
L = −E log σ( β · [ log πθ(yw|x) − log πref(yw|x)
                  − log πθ(yl|x) + log πref(yl|x) ] )
```

BC itself is an afternoon of PyTorch — no environment, no bootstrapping:

```python
logp = policy.log_prob(expert_actions)      # MSE for continuous actions
loss = -logp.mean()
optimizer.zero_grad(set_to_none=True)
loss.backward()
optimizer.step()
```

| Method | Needs | Interaction | Ceiling | Typical use |
|---|---|---|---|---|
| BC | expert state-action data | none | expert level | first move, demos exist |
| DAgger | expert relabelling rounds | learner's own rollouts | expert level | robotics, covariate shift |
| GAIL | expert transitions | env + discriminator | expert distribution | no reward, sim is cheap |
| RLHF-PPO | pairs + reward model | heavy sampling | above experts | LLM alignment |
| DPO | preference pairs only | none | preference-consistent | LLM alignment, cheap |
| GRPO | verifier or reward model, groups | grouped sampling | verifier's notion | reasoning, verifiable tasks |

**Advantages.** The reward-function problem disappears: demonstrations
and pairwise choices are what humans produce naturally. DPO and GRPO
remove the most fragile machinery in the stack — no PPO
hyperparameters, no critic to drift, no reward model quietly becoming
the objective. BC gives a working baseline in an afternoon and beats
an untuned RL attempt on day one.

**Disadvantages.** BC inherits the expert and cannot exceed them, and
its errors compound: one wrong action lands in a state the expert
never showed, where the next prediction is worse — the covariate-shift
ratchet, which DAgger pays interaction to undo. Preference data is
expensive, noisy and biased toward whatever the labeller noticed last.
DPO leans entirely on the reference model and β: drift too far and
fluency collapses; GRPO still needs a trustworthy verifier or reward
model, so the specification problem moves rather than vanishes. Every
method here is only as honest as its data — cloning a flawed expert
automates the flaw.
