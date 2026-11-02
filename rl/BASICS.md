# Reinforcement learning basics

This is a first pass through reinforcement learning (RL): the main idea,
the vocabulary, a small example and the shape of a learning algorithm.
It assumes no prior RL knowledge. For neural-network methods and their
trade-offs, continue to the [deep RL reference](README.md); for robot
deployment, see the [physical robotics guide](ROBOTICS.md).

## The short version

An agent repeatedly chooses an action, sees what happens, and receives a
reward. It learns a policy — a rule for choosing actions — that earns more
reward over time. Unlike supervised learning, it is not given the correct
action for every situation. It has to discover useful behaviour through
interaction.

```text
agent --action--> environment
agent <--next state, reward-- environment
```

The challenge is that an action can have consequences much later. A robot
that turns left may not learn whether that was helpful until it reaches
the target, or falls over several seconds later.

## A small example: a grid-world

Imagine a character moving through a grid to reach a goal. It can move up,
down, left or right. Each move costs a little reward; reaching the goal
earns a larger reward. The agent must learn a route that gets to the goal
without taking unnecessary steps.

| RL term | In the grid-world |
|---|---|
| Agent | The learning character |
| Environment | The grid and its rules |
| State | The character's current position (and any other information needed to decide) |
| Action | Move up, down, left or right |
| Reward | A small step cost, a wall penalty or the goal reward |
| Episode | One attempt, from starting position until goal or time limit |
| Policy | The rule that picks a move for each state |

The agent observes its position, chooses a move, then receives a new
position and reward. One such record is a **transition**:

```text
(state, action, reward, next_state, ended)
```

An episode is a sequence of transitions. The agent improves by collecting
many episodes, not by receiving a label that says “move right” at each
step.

## Reward, return and the long-term goal

A **reward** is the immediate score from one step. A **return** is the
total reward from now onward. If `r_t` is the reward at time `t` and `γ`
(gamma) is a discount between 0 and 1, the discounted return is:

```text
G_t = r_t + γ r_(t+1) + γ² r_(t+2) + ...
```

For example, with `γ = 0.9`, an immediate reward of `-0.1`, another
`-0.1` next step, and then a goal reward of `+1` gives:

```text
G_t = -0.1 + 0.9 × (-0.1) + 0.9² × 1 = 0.62
```

Discounting makes distant rewards count a little less and keeps an
infinite-horizon sum finite. A high gamma values long-term outcomes more;
a lower gamma emphasizes nearer rewards. The learning objective is
usually to maximize **expected** return, because actions and environments
may be random.

The reward is the specification of what “good” means. If it rewards
distance traveled, an agent may run in circles; if it rewards task
completion, it may find a useful route. This is called **reward
misspecification** or **reward hacking**: the agent optimizes the score
provided, not the intention behind it. Design and test rewards as
carefully as the policy.

## Policy, value and action-value

A **policy**, written `π(a | s)`, describes how the agent chooses action
`a` in state `s`. It can choose one action deterministically or assign a
probability to each action.

A **value function** estimates future return:

| Quantity | Question it answers |
|---|---|
| `V(s)` | How much return should I expect from this state if I follow my policy? |
| `Q(s, a)` | How much return should I expect if I take this action now, then follow my policy? |
| `A(s, a) = Q(s, a) − V(s)` | How much better or worse is this action than the state's usual expectation? |

Values help an agent compare choices before it has reached the end of an
episode. They are estimates, not guarantees: a high-value action can still
lead to a bad outcome when the environment is stochastic.

## Exploration and exploitation

The agent faces a trade-off:

- **Exploration:** try actions to learn what they do.
- **Exploitation:** choose the action currently believed to be best.

Always exploiting can trap the agent in a mediocre choice discovered
early. Exploring forever wastes reward on actions it already understands.
One simple strategy for discrete actions is **epsilon-greedy**: with
probability `ε`, choose a random action; otherwise choose the action with
the highest estimated value. `ε` is often reduced during training, while
still keeping some exploration.

In real systems, exploration can be costly or dangerous. A simulator,
offline data or a constrained experiment may be preferable to letting a
new policy freely try actions on people, production systems or physical
hardware.

## How learning from experience works

Many RL methods repeat this loop:

1. Observe the current state.
2. Choose and execute an action.
3. Record the reward and next state.
4. Use the experience to improve the policy or its value estimates.
5. Repeat, then evaluate the updated agent on fresh episodes.

The agent does not need to wait for the final outcome every time. It can
estimate the value of a next state and use that estimate as a **bootstrap**
target. Tabular Q-learning uses this update:

```text
target = reward + γ × max_a' Q(next_state, a')
Q(state, action) += α × (target − Q(state, action))
```

Here `α` (alpha) is the learning rate: how much to change the old estimate
toward the new target. If the transition ended the episode, there is no
future value to add, so `target = reward`.

This update says: revise the value of the action just taken toward the
reward received plus the best estimated future value. The difference
between the target and the old estimate is the **temporal-difference
error**. Deep Q-Networks (DQN) use the same basic idea but represent `Q`
with a neural network instead of a table.

## Common families of methods

| Family | Main idea | Examples |
|---|---|---|
| Value-based | Learn action values, then choose actions with high values | Q-learning, DQN |
| Policy-based | Directly optimize the policy's action probabilities | REINFORCE |
| Actor–critic | Learn a policy (actor) and a value estimate (critic) together | A2C, PPO, SAC |
| Model-based | Learn or use a model of the environment, then plan with it | Model-predictive control, learned-dynamics methods |
| Offline RL | Learn from a fixed dataset without collecting new transitions during training | IQL, conservative Q-learning methods |

These names are a map, not a recipe. Which method works depends on the
action space, data available, cost of interaction, reward quality and
stability requirements. The [deep RL reference](README.md) compares the
major algorithms and explains their mechanics.

## When RL is the right tool

RL is most useful when all of these are true:

- Decisions happen in sequence, and today's action affects later choices.
- The outcome is delayed or depends on a whole sequence of actions.
- You can obtain a reward signal, but do not have the correct action label
  for each state.
- There is a safe, affordable way to gather enough experience or a useful
  fixed dataset.

If you already have examples of the correct answer, supervised learning
is usually simpler. If each decision is independent and only its immediate
reward matters, a **contextual bandit** may be enough; it is a simpler
setting than full RL because actions do not change future states.

## Evaluate more than one training score

RL training is noisy: two runs of the same algorithm can learn at
different speeds or reach different results. A single high score is not
strong evidence. For a first experiment:

1. Record a simple baseline, such as a random policy or existing
   controller.
2. Evaluate the trained policy without exploration noise.
3. Run several independent random seeds and report the spread, not just
   the best run.
4. Test on held-out initial conditions or environment variations.
5. Check safety and failure cases as well as average reward.

Also distinguish an episode that **terminated** because the task finished
from one that was **truncated** by a time limit. That difference affects
whether an algorithm should bootstrap from the next state.

## Where to go next

- Read the [deep reinforcement learning reference](README.md) for
  Bellman equations, DQN, policy gradients, PPO, SAC, offline RL and
  practical training details.
- Read [deep RL for physical robots](ROBOTICS.md) for task formulation,
  simulation-to-real transfer and deployment constraints.
- Use the [learning path](../LEARNING_PATH.md) to place RL alongside the
  rest of the systems and machine-learning material.
